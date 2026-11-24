package main

import "time"

//Байтовые константы стандарта SOCKS5
const (
	socksVersion5 = 0x05 //1 треб
	authNoAuth = 0x00 //4 требование
	authNoAccept = 0xFF //4 требование
	cmdConnect = 0x01 //3 требование
	atypIPv4 = 0x01 //1 требование
	atypDomainName = 0x03 //10 требование
	atypIPv6 = 0x04 //4 требование
	repSucceeded = 0x00 //1 требование
	repGeneralFail = 0x01
	repHostUnreach = 0x04 //10 требование
	repConnRefused = 0x05
	repCmdNotSupp = 0x07 //3 требование
	repAtypNotSupp = 0x08 //4 требование
)

type SessionState int

//Этапы жизни одного соединения для одного клиента
const (
	StateGreetingRead SessionState = iota //ждем от клиента список методов авторизации
	StateGreetingWrite //отправляем клиенту выбранный метод
	StateRequestRead //ждем команду коннект и адрес назначения
	StateDNSResolving //отправили юдп запрос в днс ждем айпи
	StateUpstreamConnecting //вызвали неблокирующий коннект к сайту, ждем завершения
	StateReplyWrite //отправляем клиенту итоговый ответ SOCKS5
	StateForwarding //тунель установлен: гоняем сырые байты туда обратно
)

type Session struct {
	clientFD int //фд сокета клиента
	upstreamFD int //фд сервера
	state SessionState

	c2u Buffer //от клиента к серверу (плюс сюда читаем handshake и запрос)
	u2c Buffer //от сервера к клиенту (плюс сюда пишем ответы SOCKS5)

	clientEOF bool
	upstreamEOF bool
	clientShutWR bool //чтобы не вызывать shutdown дважды
	upstreamShutWR bool

	targetPort uint16
	dnsID uint16
	dnsDomain string //домен для повторов при таймауте (10 требование)
	dnsAttempts int //счетчик попыток DNS-запроса (максимум 3)
	dnsSentAt time.Time
	connectAt time.Time //когда начали connect (для таймаута)

	lastEpoch uint64 //номер итерации Poll, чтобы не добавлять сессию дважды без мапы
}