package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const connectTimeout = 10 * time.Second

type ProxyServer struct {
	listenFD int
	dns *DNSResolver
	sessions map[int]*Session //поиск сессии по clientFD или upstreamFD
	epoch uint64 //счетчик итераций цикла (чтобы не аллоцировать map на каждом шаге)
}

//поднимаем неблокирующий слушающий сокет и DNS-резолвер
func newProxyServer(port int, customDNSIP net.IP, customDNSPort int) (*ProxyServer, error) {
	//создаем слушающий тсп-сокет
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	if err := unix.SetNonblock(fd, true); err != nil { //флаг для не блокировки потока (5 требование)
		unix.Close(fd)
		return nil, err
	}

	//привязываем сокет к 0.0.0.0:<port> и переводим в режим прослушивания (2 требование)
	sa := &unix.SockaddrInet4{Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, err
	}

	dns, err := newDNSResolver(customDNSIP, customDNSPort) //10 требование
	if err != nil {
		unix.Close(fd)
		return nil, err
	}

	return &ProxyServer{
		listenFD: fd,
		dns: dns,
		sessions: make(map[int]*Session),
	}, nil
}

//главный цикл событий на системном Poll (9 требование)
func (s *ProxyServer) Run() {
	var pfds []unix.PollFd

	for {
		s.epoch++
		pfds = pfds[:0]

		//Слушающий сокет и DNS-сокет добавляем всегда с флагом POLLIN (10 требование)
		pfds = append(pfds, unix.PollFd{Fd: int32(s.listenFD), Events: unix.POLLIN})
		pfds = append(pfds, unix.PollFd{Fd: int32(s.dns.fd), Events: unix.POLLIN})

		hasActiveTimers := len(s.dns.pending) > 0

		//добавляем клиентские и серверные сокеты только если реально есть что читать/писать
		//(иначе будут холостые просыпания Poll) 7 требование
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
			case StateForwarding: //6 дуплекс требование прокси->клиенту
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
				case StateForwarding: //6 требование клиент->прокси
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

		//7 требование
		//если нет активных таймеров, спим бесконечно до первого события 
		timeoutMs := -1
		if hasActiveTimers {
			timeoutMs = 200
		}

		n, err := unix.Poll(pfds, timeoutMs) //единственный блокирующий вызов 5 требование
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
			if pfd.Revents == 0 { //ничего не произошло
				continue
			}
			fd := int(pfd.Fd)

			//выбираем нужный обработчик
			if fd == s.listenFD {
				s.handleAccept()
			} else if fd == s.dns.fd {
				s.handleDNSRead() //10 требование
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
		if err := unix.SetNonblock(nfd, true); err != nil { //5 требование
			unix.Close(nfd)
			continue
		}
		sess := &Session{
			clientFD: nfd,
			upstreamFD: -1,
			state: StateGreetingRead,
		}
		s.sessions[nfd] = sess
	}
}

//читаем готовые DNS-ответы из UDP-сокета (10 требование)
func (s *ProxyServer) handleDNSRead() {
	for {
		n, _, err := unix.Recvfrom(s.dns.fd, s.dns.recvBuf[:], 0) //Читает одну входящую UDP-датаграмму в заранее выделенный массив
		if err != nil {
			if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
				fmt.Fprintf(os.Stderr, "DNS recv error: %v\n", err)
			}
			return
		}
		id, ip, parseErr := parseDNSResponse(s.dns.recvBuf[:n]) //парсинг ответа
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

//чистим зависшие DNS-запросы (до 3 попыток, 10 требование) и долгие попытки подключения
func (s *ProxyServer) checkTimeouts() {
	now := time.Now()

	var timedOutDNS []*Session
	for _, sess := range s.dns.pending { //проверка зависших
		if now.Sub(sess.dnsSentAt) > dnsQueryTimeout {
			timedOutDNS = append(timedOutDNS, sess)
		}
	}

	for _, sess := range timedOutDNS {
		if sess.dnsAttempts < dnsMaxAttempts { //повторяем
			if err := s.dns.sendAttempt(sess); err != nil {
				s.dns.Cancel(sess)
				s.sendReplyAndClose(sess, repHostUnreach)
			}
		} else {
			s.dns.Cancel(sess)
			s.sendReplyAndClose(sess, repHostUnreach)
		}
	}

	for _, sess := range s.sessions { //долгие подключения
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

	if revents&(unix.POLLIN|unix.POLLHUP) != 0 { //чтение входных данных от клиента
		var tmp [bufSize]byte
		avail := sess.c2u.Avail()
		if avail > 0 {
			n, err := unix.Read(sess.clientFD, tmp[:avail]) //6 требование
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
	case StateGreetingRead: //ждем приветствие от SOCKS5
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processGreeting(sess)

	case StateGreetingWrite: //отправляем ответ
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

	case StateRequestRead: //ждем команду коннект и адрес
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processRequest(sess)

	case StateReplyWrite: //отправляем клиенту итог подключения
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

	case StateForwarding: //пересылка данных от сайта браузеру (6 требование)
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
	if data[0] != socksVersion5 { //1 требование
		s.closeSession(sess)
		return
	}
	nMethods := int(data[1])
	if len(data) < 2+nMethods {
		return //ждем пока доедут все методы
	}

	hasNoAuth := false
	for i := 0; i < nMethods; i++ { //ищет вход без пароля
		if data[2+i] == authNoAuth {
			hasNoAuth = true
			break
		}
	}
	sess.c2u.Consume(2 + nMethods) //очищает буфер

	if !hasNoAuth { //4 требование
		sess.u2c.Write([]byte{socksVersion5, authNoAccept})
		sess.state = StateReplyWrite
		return
	}

	sess.u2c.Write([]byte{socksVersion5, authNoAuth}) //ответ
	sess.state = StateGreetingWrite
}

//разбираем команду CONNECT и адрес назначения (IPv4 или домен)
func (s *ProxyServer) processRequest(sess *Session) {
	data := sess.c2u.Bytes()
	if len(data) < 4 {
		return
	}
	if data[0] != socksVersion5 { //1 требование
		s.closeSession(sess)
		return
	}
	cmd := data[1]
	atyp := data[3]

	var reqLen int
	var ipv4 [4]byte
	var domain string

	switch atyp {
	case atypIPv4: //1 требование
		reqLen = 4 + 4 + 2
		if len(data) < reqLen {
			return
		}
		copy(ipv4[:], data[4:8])
		sess.targetPort = binary.BigEndian.Uint16(data[8:10])

	case atypDomainName: //10 требование
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

	default: //4 требование
		s.sendReplyAndClose(sess, repAtypNotSupp)
		return
	}

	sess.c2u.Consume(reqLen)

	if cmd != cmdConnect { //3 требование
		s.sendReplyAndClose(sess, repCmdNotSupp) //поддерживаем только CONNECT (0x01)
		return
	}

	if atyp == atypIPv4 { //10 требование
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
	ufd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP) //неблокирующий тсп сокет
	if err != nil {
		s.sendReplyAndClose(sess, repGeneralFail)
		return
	}
	if err := unix.SetNonblock(ufd, true); err != nil { //5 требование
		unix.Close(ufd)
		s.sendReplyAndClose(sess, repGeneralFail)
		return
	}

	sa := &unix.SockaddrInet4{
		Port: int(sess.targetPort),
		Addr: ip,
	}

	err = unix.Connect(ufd, sa) //попытка соединения
	if err == nil {
		sess.upstreamFD = ufd
		s.sessions[ufd] = sess
		s.queueSuccessReply(sess)
		return
	}

	if errors.Is(err, unix.EINPROGRESS) { //норм
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

	if revents&(unix.POLLERR|unix.POLLNVAL) != 0 { //обработка отрыва
		s.closeSession(sess)
		return
	}

	if revents&(unix.POLLIN|unix.POLLHUP) != 0 { //чтение ответа от сайта (6 требование)
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

	if revents&unix.POLLOUT != 0 && sess.c2u.Len() > 0 { //6 требование
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

//6 требование
//мягкий half-close: закрываем запись когда буфер опустел, а оба сокета — когда всё дошло
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

func (s *ProxyServer) queueSuccessReply(sess *Session) { //собирает ответ успеха (1 требование)
	reply := []byte{socksVersion5, repSucceeded, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	sess.u2c.Write(reply)
	sess.state = StateReplyWrite
}

func (s *ProxyServer) sendReplyAndClose(sess *Session, rep byte) { //собирает пакет ошибки (1 требование)
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