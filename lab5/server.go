package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const connectTimeout = 10 * time.Second

type ProxyServer struct {
	listenFD int
	dns      *DNSResolver
	sessions map[int]*Session //поиск сессии по clientFD или upstreamFD
	epoch    uint64           //счетчик итераций цикла (чтобы не аллоцировать map на каждом шаге)
}

//поднимаем неблокирующий слушающий сокет и DNS-резолвер
func newProxyServer(port int) (*ProxyServer, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}

	sa := &unix.SockaddrInet4{Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, err
	}

	dns, err := newDNSResolver()
	if err != nil {
		unix.Close(fd)
		return nil, err
	}

	return &ProxyServer{
		listenFD: fd,
		dns:      dns,
		sessions: make(map[int]*Session),
	}, nil
}

//главный цикл событий на системном Poll
func (s *ProxyServer) Run() {
	var pfds []unix.PollFd

	for {
		s.epoch++
		pfds = pfds[:0]

		//слушающий сокет и DNS всегда слушаем на чтение
		pfds = append(pfds, unix.PollFd{Fd: int32(s.listenFD), Events: unix.POLLIN})
		pfds = append(pfds, unix.PollFd{Fd: int32(s.dns.fd), Events: unix.POLLIN})

		hasActiveTimers := len(s.dns.pending) > 0

		//добавляем клиентские и серверные сокеты только если реально есть что читать/писать
		//(иначе будут холостые просыпания Poll)
		for _, sess := range s.sessions {
			if sess.lastEpoch == s.epoch {
				continue
			}
			sess.lastEpoch = s.epoch

			if sess.state == StateUpstreamConnecting {
				hasActiveTimers = true
			}

			var cEvents int16
			switch sess.state {
			case StateGreetingRead, StateRequestRead:
				if sess.c2u.Avail() > 0 {
					cEvents |= unix.POLLIN
				}
			case StateGreetingWrite, StateReplyWrite:
				if sess.u2c.Len() > 0 {
					cEvents |= unix.POLLOUT
				}
			case StateForwarding:
				//читаем только если есть место в буфере (backpressure)
				if !sess.clientEOF && sess.c2u.Avail() > 0 {
					cEvents |= unix.POLLIN
				}
				if sess.u2c.Len() > 0 {
					cEvents |= unix.POLLOUT
				}
			}
			if cEvents != 0 {
				pfds = append(pfds, unix.PollFd{Fd: int32(sess.clientFD), Events: cEvents})
			}

			if sess.upstreamFD != -1 {
				var uEvents int16
				switch sess.state {
				case StateUpstreamConnecting:
					uEvents |= unix.POLLOUT //неблокирующий connect сигналит через POLLOUT
				case StateForwarding:
					if !sess.upstreamEOF && sess.u2c.Avail() > 0 {
						uEvents |= unix.POLLIN
					}
					if sess.c2u.Len() > 0 {
						uEvents |= unix.POLLOUT
					}
				}
				if uEvents != 0 {
					pfds = append(pfds, unix.PollFd{Fd: int32(sess.upstreamFD), Events: uEvents})
				}
			}
		}

		//если нет активных таймеров — спим бесконечно до первого события
		timeoutMs := -1
		if hasActiveTimers {
			timeoutMs = 500
		}

		n, err := unix.Poll(pfds, timeoutMs)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			fmt.Fprintf(os.Stderr, "Poll error: %v\n", err)
			return
		}

		if hasActiveTimers {
			s.checkTimeouts()
		}

		if n == 0 {
			continue
		}

		for i := range pfds {
			pfd := &pfds[i]
			if pfd.Revents == 0 {
				continue
			}
			fd := int(pfd.Fd)

			if fd == s.listenFD {
				s.handleAccept()
			} else if fd == s.dns.fd {
				s.handleDNSRead()
			} else if sess, ok := s.sessions[fd]; ok {
				if fd == sess.clientFD {
					s.handleClientEvent(sess, pfd.Revents)
				} else if fd == sess.upstreamFD {
					s.handleUpstreamEvent(sess, pfd.Revents)
				}
			}
		}
	}
}

//принимаем всех новых клиентов из очереди
func (s *ProxyServer) handleAccept() {
	for {
		nfd, _, err := unix.Accept(s.listenFD)
		if err != nil {
			if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
				fmt.Fprintf(os.Stderr, "Accept error: %v\n", err)
			}
			return
		}
		if err := unix.SetNonblock(nfd, true); err != nil {
			unix.Close(nfd)
			continue
		}
		sess := &Session{
			clientFD:   nfd,
			upstreamFD: -1,
			state:      StateGreetingRead,
		}
		s.sessions[nfd] = sess
	}
}

//читаем готовые DNS-ответы из UDP-сокета
func (s *ProxyServer) handleDNSRead() {
	for {
		n, _, err := unix.Recvfrom(s.dns.fd, s.dns.recvBuf[:], 0)
		if err != nil {
			if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
				fmt.Fprintf(os.Stderr, "DNS recv error: %v\n", err)
			}
			return
		}
		id, ip, parseErr := parseDNSResponse(s.dns.recvBuf[:n])
		sess, exists := s.dns.pending[id]
		if !exists {
			continue //чужой или уже протухший ответ
		}
		delete(s.dns.pending, id)
		sess.dnsID = 0

		if parseErr != nil {
			s.sendReplyAndClose(sess, repHostUnreach)
			continue
		}
		s.startUpstreamConnect(sess, ip)
	}
}

//чистим зависшие DNS-запросы и долгие попытки подключения
func (s *ProxyServer) checkTimeouts() {
	now := time.Now()

	for id, sess := range s.dns.pending {
		if now.Sub(sess.dnsSentAt) > dnsQueryTimeout {
			delete(s.dns.pending, id)
			sess.dnsID = 0
			s.sendReplyAndClose(sess, repHostUnreach)
		}
	}

	for _, sess := range s.sessions {
		if sess.state == StateUpstreamConnecting && now.Sub(sess.connectAt) > connectTimeout {
			delete(s.sessions, sess.upstreamFD)
			unix.Close(sess.upstreamFD)
			sess.upstreamFD = -1
			s.sendReplyAndClose(sess, repHostUnreach)
		}
	}
}

//обработка событий на сокете клиента
func (s *ProxyServer) handleClientEvent(sess *Session, revents int16) {
	if revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		s.closeSession(sess)
		return
	}

	if revents&(unix.POLLIN|unix.POLLHUP) != 0 {
		var tmp [bufSize]byte
		avail := sess.c2u.Avail()
		if avail > 0 {
			n, err := unix.Read(sess.clientFD, tmp[:avail])
			if n > 0 {
				sess.c2u.Write(tmp[:n])
			} else if n == 0 || (revents&unix.POLLHUP != 0 && (errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK))) {
				sess.clientEOF = true
			} else if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
	}

	switch sess.state {
	case StateGreetingRead:
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processGreeting(sess)

	case StateGreetingWrite:
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 {
			n, err := unix.Write(sess.clientFD, sess.u2c.Bytes())
			if n > 0 {
				sess.u2c.Consume(n)
			} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
		if sess.u2c.Len() == 0 {
			if sess.clientEOF {
				s.closeSession(sess)
				return
			}
			sess.state = StateRequestRead
			s.processRequest(sess) //вдруг клиент уже прислал запрос вместе с приветствием
		}

	case StateRequestRead:
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processRequest(sess)

	case StateReplyWrite:
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 {
			n, err := unix.Write(sess.clientFD, sess.u2c.Bytes())
			if n > 0 {
				sess.u2c.Consume(n)
			} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
		if sess.u2c.Len() == 0 {
			if sess.upstreamFD == -1 {
				s.closeSession(sess) //отправляли ошибку — закрываемся
				return
			}
			sess.state = StateForwarding
		}

	case StateForwarding:
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 {
			n, err := unix.Write(sess.clientFD, sess.u2c.Bytes())
			if n > 0 {
				sess.u2c.Consume(n)
			} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
		s.checkHalfCloseAndFinish(sess)
	}
}

//разбираем приветствие SOCKS5 (выбираем метод без аутентификации 0x00)
func (s *ProxyServer) processGreeting(sess *Session) {
	data := sess.c2u.Bytes()
	if len(data) < 2 {
		return
	}
	if data[0] != socksVersion5 {
		s.closeSession(sess)
		return
	}
	nMethods := int(data[1])
	if len(data) < 2+nMethods {
		return //ждем пока доедут все методы
	}

	hasNoAuth := false
	for i := 0; i < nMethods; i++ {
		if data[2+i] == authNoAuth {
			hasNoAuth = true
			break
		}
	}
	sess.c2u.Consume(2 + nMethods)

	if !hasNoAuth {
		sess.u2c.Write([]byte{socksVersion5, authNoAccept})
		sess.state = StateReplyWrite
		return
	}

	sess.u2c.Write([]byte{socksVersion5, authNoAuth})
	sess.state = StateGreetingWrite
}

//разбираем команду CONNECT и адрес назначения (IPv4 или домен)
func (s *ProxyServer) processRequest(sess *Session) {
	data := sess.c2u.Bytes()
	if len(data) < 4 {
		return
	}
	if data[0] != socksVersion5 {
		s.closeSession(sess)
		return
	}
	cmd := data[1]
	atyp := data[3]

	var reqLen int
	var ipv4 [4]byte
	var domain string

	switch atyp {
	case atypIPv4:
		reqLen = 4 + 4 + 2
		if len(data) < reqLen {
			return
		}
		copy(ipv4[:], data[4:8])
		sess.targetPort = binary.BigEndian.Uint16(data[8:10])

	case atypDomainName:
		if len(data) < 5 {
			return
		}
		dLen := int(data[4])
		reqLen = 4 + 1 + dLen + 2
		if len(data) < reqLen {
			return
		}
		domain = string(data[5 : 5+dLen])
		sess.targetPort = binary.BigEndian.Uint16(data[5+dLen : reqLen])

	case atypIPv6:
		s.sendReplyAndClose(sess, repAtypNotSupp) //IPv6 по условию не поддерживаем
		return

	default:
		s.sendReplyAndClose(sess, repAtypNotSupp)
		return
	}

	sess.c2u.Consume(reqLen)

	if cmd != cmdConnect {
		s.sendReplyAndClose(sess, repCmdNotSupp) //поддерживаем только CONNECT (0x01)
		return
	}

	if atyp == atypIPv4 {
		s.startUpstreamConnect(sess, ipv4)
	} else {
		sess.state = StateDNSResolving
		if err := s.dns.Resolve(domain, sess); err != nil {
			s.sendReplyAndClose(sess, repGeneralFail)
		}
	}
}

//запускаем неблокирующее подключение к целевому серверу
func (s *ProxyServer) startUpstreamConnect(sess *Session, ip [4]byte) {
	ufd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		s.sendReplyAndClose(sess, repGeneralFail)
		return
	}
	if err := unix.SetNonblock(ufd, true); err != nil {
		unix.Close(ufd)
		s.sendReplyAndClose(sess, repGeneralFail)
		return
	}

	sa := &unix.SockaddrInet4{
		Port: int(sess.targetPort),
		Addr: ip,
	}

	err = unix.Connect(ufd, sa)
	if err == nil {
		sess.upstreamFD = ufd
		s.sessions[ufd] = sess
		s.queueSuccessReply(sess)
		return
	}

	if errors.Is(err, unix.EINPROGRESS) {
		sess.upstreamFD = ufd
		sess.connectAt = time.Now()
		s.sessions[ufd] = sess
		sess.state = StateUpstreamConnecting
		return
	}

	unix.Close(ufd)
	s.sendReplyAndClose(sess, mapConnectError(err))
}

//обработка событий на сокете целевого сервера
func (s *ProxyServer) handleUpstreamEvent(sess *Session, revents int16) {
	//проверяем результат неблокирующего connect через SO_ERROR
	if sess.state == StateUpstreamConnecting {
		soErr, err := unix.GetsockoptInt(sess.upstreamFD, unix.SOL_SOCKET, unix.SO_ERROR)
		if err != nil || soErr != 0 {
			delete(s.sessions, sess.upstreamFD)
			unix.Close(sess.upstreamFD)
			sess.upstreamFD = -1
			s.sendReplyAndClose(sess, mapConnectError(unix.Errno(soErr)))
			return
		}
		s.queueSuccessReply(sess)
		return
	}

	if revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		s.closeSession(sess)
		return
	}

	if revents&(unix.POLLIN|unix.POLLHUP) != 0 {
		var tmp [bufSize]byte
		avail := sess.u2c.Avail()
		if avail > 0 {
			n, err := unix.Read(sess.upstreamFD, tmp[:avail])
			if n > 0 {
				sess.u2c.Write(tmp[:n])
			} else if n == 0 || (revents&unix.POLLHUP != 0 && (errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK))) {
				sess.upstreamEOF = true
			} else if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
	}

	if revents&unix.POLLOUT != 0 && sess.c2u.Len() > 0 {
		n, err := unix.Write(sess.upstreamFD, sess.c2u.Bytes())
		if n > 0 {
			sess.c2u.Consume(n)
		} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			s.closeSession(sess)
			return
		}
	}

	s.checkHalfCloseAndFinish(sess)
}

//аккуратный half-close: закрываем запись когда буфер опустел, а оба сокета — когда всё доехало
func (s *ProxyServer) checkHalfCloseAndFinish(sess *Session) {
	if sess.clientEOF && sess.c2u.Len() == 0 && sess.upstreamFD != -1 && !sess.upstreamShutWR {
		_ = unix.Shutdown(sess.upstreamFD, unix.SHUT_WR)
		sess.upstreamShutWR = true
	}
	if sess.upstreamEOF && sess.u2c.Len() == 0 && sess.clientFD != -1 && !sess.clientShutWR {
		_ = unix.Shutdown(sess.clientFD, unix.SHUT_WR)
		sess.clientShutWR = true
	}
	if sess.clientEOF && sess.upstreamEOF && sess.c2u.Len() == 0 && sess.u2c.Len() == 0 {
		s.closeSession(sess)
	}
}

func (s *ProxyServer) queueSuccessReply(sess *Session) {
	reply := []byte{socksVersion5, repSucceeded, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	sess.u2c.Write(reply)
	sess.state = StateReplyWrite
}

func (s *ProxyServer) sendReplyAndClose(sess *Session, rep byte) {
	reply := []byte{socksVersion5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	sess.u2c.Write(reply)
	sess.state = StateReplyWrite
}

//освобождаем дескрипторы и убираем сессию из мапы
func (s *ProxyServer) closeSession(sess *Session) {
	s.dns.Cancel(sess)
	if sess.clientFD != -1 {
		delete(s.sessions, sess.clientFD)
		unix.Close(sess.clientFD)
		sess.clientFD = -1
	}
	if sess.upstreamFD != -1 {
		delete(s.sessions, sess.upstreamFD)
		unix.Close(sess.upstreamFD)
		sess.upstreamFD = -1
	}
}

//переводим системную ошибку connect в код ответа SOCKS5
func mapConnectError(err error) byte {
	if errors.Is(err, unix.ECONNREFUSED) {
		return repConnRefused
	}
	if errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH) {
		return repHostUnreach
	}
	return repGeneralFail
}