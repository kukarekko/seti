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
	sessions map[int]*Session //активные сессии
	epoch uint64
}

//поднимает неблокирующий слушающий сокет и DNS-резолвер
func newProxyServer(port int, customDNSIP net.IP, customDNSPort int) (*ProxyServer, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1) //разрешает перзапускать прокси на том же порту
	if err := unix.SetNonblock(fd, true); err != nil { //5 требование
		unix.Close(fd)
		return nil, err
	}
	sa := &unix.SockaddrInet4{Port: port}
	if err := unix.Bind(fd, sa); err != nil { //2 требование //привязывает сокет к выбранному порту 
		unix.Close(fd)
		return nil, err
	}
	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, err
	}
	dns, err := newDNSResolver(customDNSIP, customDNSPort) //10 требование иниц общий неблок юдп-резолвер
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

//9 требование
func (s *ProxyServer) Run() {
	var pfds []unix.PollFd
	for {
		s.epoch++
		pfds = pfds[:0]
		pfds = append(pfds, unix.PollFd{Fd: int32(s.listenFD), Events: unix.POLLIN}) //подпис на новые подключ
		pfds = append(pfds, unix.PollFd{Fd: int32(s.dns.fd), Events: unix.POLLIN}) //10 требование, на ответы от днс
		hasActiveTimers := len(s.dns.pending) > 0

		//добавляем сокеты только если реально есть что читать или писать, 7 требование
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
				if !sess.clientEOF && sess.c2u.Avail() > 0 {
					cEvents |= unix.POLLIN 
				}
			case StateGreetingWrite, StateReplyWrite:
				if sess.u2c.Len() > 0 {
					cEvents |= unix.POLLOUT 
				}
			case StateForwarding: //6 требование режим прозрачной перессылки
				if !sess.clientEOF && sess.c2u.Avail() > 0 {
					cEvents |= unix.POLLIN 
				}
				if sess.u2c.Len() > 0 {
					cEvents |= unix.POLLOUT
				}
			}
			if cEvents != 0 { //если клиенту действительно нужно писать или читать
				pfds = append(pfds, unix.PollFd{Fd: int32(sess.clientFD), Events: cEvents}) //добавляем фд в poll
			}
			if sess.upstreamFD != -1 {
				var uEvents int16
				switch sess.state {
				case StateUpstreamConnecting:
					uEvents |= unix.POLLOUT
				case StateForwarding: //6 требование
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
		//без активных таймеров спим до события, 7 требование
		timeoutMs := -1
		if hasActiveTimers {
			timeoutMs = 200
		}
		n, err := unix.Poll(pfds, timeoutMs) //5 требование блокируем поток в ядре до наступления события 
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			fmt.Fprintf(os.Stderr, "Poll error: %v\n", err)
			return
		}
		if hasActiveTimers {
			s.checkTimeouts() //проверяем просроченные днс запросы и зависшые connect
		}
		if n == 0 {
			continue
		}
		for i := range pfds { //по всем дескрипторам
			pfd := &pfds[i]
			if pfd.Revents == 0 {
				continue
			}
			fd := int(pfd.Fd)
			if fd == s.listenFD {
				s.handleAccept() //принимаем новых клиентов 
			} else if fd == s.dns.fd {
				s.handleDNSRead() //10 требование читаем ответы от днс сервера
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
//принимает всех новых клиентов из очереди
func (s *ProxyServer) handleAccept() {
	for {
		nfd, _, err := unix.Accept(s.listenFD)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				fmt.Fprintf(os.Stderr, "Accept error: %v\n", err)
			}
			return
		}
		if err := unix.SetNonblock(nfd, true); err != nil { //5 требование
			unix.Close(nfd)
			continue
		}
		sess := &Session{ //новая сессия для подключившегося клиента
			clientFD: nfd,
			upstreamFD: -1,
			state: StateGreetingRead,
		}
		s.sessions[nfd] = sess
	}
}

//читает DNS-ответы с проверкой адреса отправителя и содержимого, 10 требование
func (s *ProxyServer) handleDNSRead() {
	for {
		n, sa, err := unix.Recvfrom(s.dns.fd, s.dns.recvBuf[:], 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				fmt.Fprintf(os.Stderr, "DNS recv error: %v\n", err)
			}
			return
		}
		//проверяем что ответ пришел именно от нашего DNS-резолвера
		from4, ok := sa.(*unix.SockaddrInet4)
		if !ok || from4.Addr != s.dns.serverAddr || from4.Port != s.dns.serverPort {
			continue
		}
		pkt := s.dns.recvBuf[:n] //реально прочит байты
		id, validHeader := extractDNSID(pkt)
		if !validHeader {
			continue
		}
		sess, exists := s.dns.pending[id] //ищем сессию которая ждет ответ с этим id
		if !exists {
			continue
		}
		ip, parseErr := parseDNSResponse(pkt, sess.dnsDomain) 
		if parseErr != nil {
			if parseErr.Error() == "not a DNS response (QR=0)" || parseErr.Error() == "question mismatch" {
				continue
			}
			delete(s.dns.pending, id)
			sess.dnsID = 0
			s.sendReplyAndClose(sess, repHostUnreach)
			continue
		}
		delete(s.dns.pending, id)
		sess.dnsID = 0
		s.startUpstreamConnect(sess, ip)
	}
}

//повторяет зависшие DNS-запросы (до 3 попыток) и закрывает долгие connect, 10 требование
func (s *ProxyServer) checkTimeouts() {
	now := time.Now() //текущее время
	var timedOutDNS []*Session //список сессий с просроченным днс запросом 
	for _, sess := range s.dns.pending { //обход все ожид днс запросы
		if now.Sub(sess.dnsSentAt) > dnsQueryTimeout {
			timedOutDNS = append(timedOutDNS, sess)
		}
	}
	for _, sess := range timedOutDNS { //обраб все просроч днс сессии
		if sess.dnsAttempts < dnsMaxAttempts { // < 3 попыток
			if err := s.dns.sendAttempt(sess); err != nil {
				s.dns.Cancel(sess)
				s.sendReplyAndClose(sess, repHostUnreach)
			}
		} else {
			s.dns.Cancel(sess)
			s.sendReplyAndClose(sess, repHostUnreach)
		}
	}
	for _, sess := range s.sessions {
		if sess.state == StateUpstreamConnecting && now.Sub(sess.connectAt) > connectTimeout { //если коннекь висит больше 10 сек
			delete(s.sessions, sess.upstreamFD)
			unix.Close(sess.upstreamFD)
			sess.upstreamFD = -1
			s.sendReplyAndClose(sess, repHostUnreach)
		}
	}
}

//обработка событий на сокете клиента
func (s *ProxyServer) handleClientEvent(sess *Session, revents int16) {
	if revents&unix.POLLNVAL != 0 {
		s.closeSession(sess)
		return
	}
	if revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 { //если есть данные для чтения или обрыв соед
		var tmp [bufSize]byte
		avail := sess.c2u.Avail()
		if avail > 0 {
			n, err := readNonblock(sess.clientFD, tmp[:avail]) //6 требование читаем не больше своб места в буфере
			if n > 0 {
				sess.c2u.Write(tmp[:n])
			} else if n == 0 {
				sess.clientEOF = true
			} else if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				if revents&(unix.POLLHUP|unix.POLLERR) != 0 {
					sess.clientEOF = true
				}
			} else {
				s.closeSession(sess)
				return
			}
		} else if revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			sess.clientEOF = true
		}
	}
	switch sess.state {
	case StateGreetingRead: //чтение приветствия 
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processGreeting(sess)
	case StateGreetingWrite: //отправка ответа на приветствие 
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 {
			n, err := writeNonblock(sess.clientFD, sess.u2c.Bytes())
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
			s.processRequest(sess)
		}
	case StateRequestRead: //чтение команды CONNECT и адреса назначения
		if sess.clientEOF && sess.c2u.Len() == 0 {
			s.closeSession(sess)
			return
		}
		s.processRequest(sess)
	case StateReplyWrite: //проверка финального ответа SOCKS5
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 {
			n, err := writeNonblock(sess.clientFD, sess.u2c.Bytes())
			if n > 0 {
				sess.u2c.Consume(n)
			} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
				s.closeSession(sess)
				return
			}
		}
		if sess.u2c.Len() == 0 {
			if sess.upstreamFD == -1 {
				s.closeSession(sess)
				return
			}
			sess.state = StateForwarding
			s.checkHalfCloseAndFinish(sess)
		}
	case StateForwarding: //6 требование основной реим перессылки трафика клиенту
		if revents&unix.POLLOUT != 0 && sess.u2c.Len() > 0 { //если готов принимать и есть данные 
			n, err := writeNonblock(sess.clientFD, sess.u2c.Bytes())
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

//разбирает приветствие SOCKS5 и выбирает метод 0x00
func (s *ProxyServer) processGreeting(sess *Session) {
	data := sess.c2u.Bytes()
	if len(data) < 2 {
		if sess.clientEOF {
			s.closeSession(sess)
		}
		return
	}
	if data[0] != socksVersion5 { //1 требование если версия протокола не 0x05
		s.closeSession(sess)
		return
	}
	nMethods := int(data[1]) //кол-во предлож клиентом методов аутентиф 
	if len(data) < 2+nMethods {
		if sess.clientEOF {
			s.closeSession(sess)
		}
		return
	}
	hasNoAuth := false
	for i := 0; i < nMethods; i++ { //ищем без аутентиф
		if data[2+i] == authNoAuth {
			hasNoAuth = true
			break
		}
	}
	sess.c2u.Consume(2 + nMethods)
	if !hasNoAuth { //4 требование если не поддерживает работу без пароля
		sess.u2c.Write([]byte{socksVersion5, authNoAccept}) //отказ
		sess.state = StateReplyWrite
		return
	}
	sess.u2c.Write([]byte{socksVersion5, authNoAuth})
	sess.state = StateGreetingWrite
}

//разбирает команду CONNECT и адрес назначения
func (s *ProxyServer) processRequest(sess *Session) {
	data := sess.c2u.Bytes()
	if len(data) < 4 {
		if sess.clientEOF {
			s.closeSession(sess)
		}
		return
	}
	if data[0] != socksVersion5 || data[2] != 0x00 { //1 требование проверяем версию 0x05 и зарезервированный байт RSV=0x00
		s.closeSession(sess)
		return
	}
	cmd := data[1]
	atyp := data[3]
	var reqLen int
	var ipv4 [4]byte
	var domain string
	switch atyp {
	case atypIPv4: //1 требование IPv4 адрес
		reqLen = 4 + 4 + 2
		if len(data) < reqLen {
			if sess.clientEOF {
				s.closeSession(sess)
			}
			return
		}
		copy(ipv4[:], data[4:8])
		sess.targetPort = binary.BigEndian.Uint16(data[8:10])
	case atypDomainName: //10 требование доменное имя 
		if len(data) < 5 {
			if sess.clientEOF {
				s.closeSession(sess)
			}
			return
		}
		dLen := int(data[4])
		if dLen == 0 {
			s.sendReplyAndClose(sess, repGeneralFail)
			return
		}
		reqLen = 4 + 1 + dLen + 2
		if len(data) < reqLen {
			if sess.clientEOF {
				s.closeSession(sess)
			}
			return
		}
		domain = string(data[5 : 5+dLen])
		sess.targetPort = binary.BigEndian.Uint16(data[5+dLen : reqLen])
	default: //4 требование (IPv6) или неизв
		s.sendReplyAndClose(sess, repAtypNotSupp)
		return
	}
	sess.c2u.Consume(reqLen)
	if cmd != cmdConnect { //3 требование если команда не коннект
		s.sendReplyAndClose(sess, repCmdNotSupp)
		return
	}
	if atyp == atypIPv4 { //10 требование если адрес задан как IPv4
		s.startUpstreamConnect(sess, ipv4)
	} else { //если задано доменное имя 
		sess.state = StateDNSResolving
		if err := s.dns.Resolve(domain, sess); err != nil {
			s.sendReplyAndClose(sess, repGeneralFail)
		}
	}
}

//запускает неблокирующее подключение к целевому серверу
func (s *ProxyServer) startUpstreamConnect(sess *Session, ip [4]byte) {
	ufd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
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
	for {
		err = unix.Connect(ufd, sa) //запускаем неблок подключение 
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
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
	if revents&unix.POLLNVAL != 0 {
		s.closeSession(sess)
		return
	}
	if sess.state == StateUpstreamConnecting { //если ждали завершения неблокируещего коннект
		soErr, err := unix.GetsockoptInt(sess.upstreamFD, unix.SOL_SOCKET, unix.SO_ERROR) //итог подключения
		if err != nil || soErr != 0 {
			delete(s.sessions, sess.upstreamFD)
			unix.Close(sess.upstreamFD)
			sess.upstreamFD = -1
			s.sendReplyAndClose(sess, mapConnectError(unix.Errno(soErr)))
			return
		}
		s.queueSuccessReply(sess) //коннект успешен - кладём ответ 0x00 в буфер клиенту
		return
	}
	if revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 { //6 требование с целевого сервера пришли данные или обрыв
		var tmp [bufSize]byte
		avail := sess.u2c.Avail()
		if avail > 0 {
			n, err := readNonblock(sess.upstreamFD, tmp[:avail])
			if n > 0 {
				sess.u2c.Write(tmp[:n])
			} else if n == 0 {
				sess.upstreamEOF = true
			} else if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				if revents&(unix.POLLHUP|unix.POLLERR) != 0 {
					sess.upstreamEOF = true
				}
			} else {
				s.closeSession(sess)
				return
			}
		} else if revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			sess.upstreamEOF = true
		}
	}
	if revents&unix.POLLOUT != 0 && sess.c2u.Len() > 0 { //6 требование если сервер готов принимать и есть данные
		n, err := writeNonblock(sess.upstreamFD, sess.c2u.Bytes())
		if n > 0 {
			sess.c2u.Consume(n)
		} else if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			s.closeSession(sess)
			return
		}
	}
	s.checkHalfCloseAndFinish(sess)
}

//мягкий half-close: закрываем запись когда буфер опустел, 6 требование
func (s *ProxyServer) checkHalfCloseAndFinish(sess *Session) {
	if sess.clientEOF && sess.c2u.Len() == 0 && sess.upstreamFD != -1 && !sess.upstreamShutWR { //клиент все дослал и закрылся 
		_ = unix.Shutdown(sess.upstreamFD, unix.SHUT_WR) //отправ фин целевому серверу но продолжает читать ответ
		sess.upstreamShutWR = true
	}
	if sess.upstreamEOF && sess.u2c.Len() == 0 && sess.clientFD != -1 && !sess.clientShutWR { //сервер все дослал и закрылся
		_ = unix.Shutdown(sess.clientFD, unix.SHUT_WR)
		sess.clientShutWR = true
	}
	if sess.clientEOF && sess.upstreamEOF && sess.c2u.Len() == 0 && sess.u2c.Len() == 0 { //оба закрылись
		s.closeSession(sess)
	}
}

//собирает ответ успеха, 1 требование
func (s *ProxyServer) queueSuccessReply(sess *Session) {
	reply := []byte{socksVersion5, repSucceeded, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	sess.u2c.Write(reply)
	sess.state = StateReplyWrite
}

//собирает пакет ошибки, 1 требование
func (s *ProxyServer) sendReplyAndClose(sess *Session, rep byte) {
	reply := []byte{socksVersion5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	sess.u2c.Write(reply)
	sess.state = StateReplyWrite
}

//освобождает дескрипторы и убирает сессию
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

//переводит системную ошибку connect в код ответа SOCKS5
func mapConnectError(err error) byte {
	if errors.Is(err, unix.ECONNREFUSED) {
		return repConnRefused
	}
	if errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH) || errors.Is(err, unix.ETIMEDOUT) {
		return repHostUnreach
	}
	return repGeneralFail
}

//чтение с повтором при прерывании сигналом
func readNonblock(fd int, buf []byte) (int, error) {
	for {
		n, err := unix.Read(fd, buf)
		if err != nil && errors.Is(err, unix.EINTR) {
			continue
		}
		return n, err
	}
}

//запись с повтором при прерывании сигналом
func writeNonblock(fd int, buf []byte) (int, error) {
	for {
		n, err := unix.Write(fd, buf)
		if err != nil && errors.Is(err, unix.EINTR) {
			continue
		}
		return n, err
	}
}