package main

import "time"

//константы протокола SOCKS5
const (
	socksVersion5  = 0x05
	authNoAuth     = 0x00
	authNoAccept   = 0xFF
	cmdConnect     = 0x01
	atypIPv4       = 0x01
	atypDomainName = 0x03
	atypIPv6       = 0x04
	repSucceeded   = 0x00
	repGeneralFail = 0x01
	repHostUnreach = 0x04
	repConnRefused = 0x05
	repCmdNotSupp  = 0x07
	repAtypNotSupp = 0x08
)

type SessionState int

//состояния конечного автомата для одного клиента
const (
	StateGreetingRead SessionState = iota
	StateGreetingWrite
	StateRequestRead
	StateDNSResolving
	StateUpstreamConnecting
	StateReplyWrite
	StateForwarding
)

type Session struct {
	clientFD   int
	upstreamFD int
	state      SessionState

	c2u Buffer //от клиента к серверу (плюс сюда читаем handshake и запрос)
	u2c Buffer //от сервера к клиенту (плюс сюда пишем ответы SOCKS5)

	clientEOF      bool
	upstreamEOF    bool
	clientShutWR   bool //чтобы не вызывать shutdown дважды
	upstreamShutWR bool

	targetPort uint16
	dnsID      uint16
	dnsSentAt  time.Time
	connectAt  time.Time //когда начали connect (для таймаута)

	lastEpoch uint64 //номер итерации Poll, чтобы не добавлять сессию дважды без мапы
}