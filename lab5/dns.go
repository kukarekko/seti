package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

//таймаут ожидания ответа от DNS
const dnsQueryTimeout = 5 * time.Second

type DNSResolver struct {
	fd       int
	serverSA unix.Sockaddr
	nextID   uint16
	pending  map[uint16]*Session //активные запросы по ID пакета
	recvBuf  [512]byte
}

//создаем один неблокирующий UDP-сокет под все DNS-запросы
func newDNSResolver() (*DNSResolver, error) {
	dnsIP := getSystemDNS()
	ip4 := dnsIP.To4()
	if ip4 == nil {
		ip4 = net.IPv4(8, 8, 8, 8).To4()
	}

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_UDP)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}

	var addr [4]byte
	copy(addr[:], ip4)
	sa := &unix.SockaddrInet4{Port: 53, Addr: addr}

	return &DNSResolver{
		fd:       fd,
		serverSA: sa,
		nextID:   1,
		pending:  make(map[uint16]*Session),
	}, nil
}

//достаем адрес резолвера из /etc/resolv.conf (если не вышло — берем 8.8.8.8)
func getSystemDNS() net.IP {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return net.IPv4(8, 8, 8, 8)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "nameserver") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				ip := net.ParseIP(fields[1])
				if ip != nil && ip.To4() != nil {
					return ip
				}
			}
		}
	}
	return net.IPv4(8, 8, 8, 8)
}

//отправляем DNS A-запрос и запоминаем сессию
func (r *DNSResolver) Resolve(domain string, sess *Session) error {
	//ищем свободный ID (чтобы не перезаписать чужой активный запрос)
	var id uint16
	found := false
	for i := 0; i < 65535; i++ {
		id = r.nextID
		r.nextID++
		if r.nextID == 0 {
			r.nextID = 1
		}
		if _, busy := r.pending[id]; !busy {
			found = true
			break
		}
	}
	if !found {
		return errors.New("dns query queue full")
	}

	pkt, err := buildDNSQuery(id, domain)
	if err != nil {
		return err
	}

	err = unix.Sendto(r.fd, pkt, 0, r.serverSA)
	if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
		return err
	}

	sess.dnsID = id
	sess.dnsSentAt = time.Now()
	r.pending[id] = sess
	return nil
}

//отменяем ожидание DNS если клиент отвалился раньше времени
func (r *DNSResolver) Cancel(sess *Session) {
	if sess.dnsID != 0 {
		delete(r.pending, sess.dnsID)
		sess.dnsID = 0
	}
}

//собираем сырой DNS-пакет с A-запросом
func buildDNSQuery(id uint16, domain string) ([]byte, error) {
	domain = strings.TrimSuffix(domain, ".")
	if len(domain) == 0 || len(domain) > 253 {
		return nil, errors.New("invalid domain length")
	}

	var buf bytes.Buffer
	//заголовок: ID, флаги (рекурсивный запрос 0x0100), 1 вопрос
	binary.Write(&buf, binary.BigEndian, id)
	binary.Write(&buf, binary.BigEndian, uint16(0x0100))
	binary.Write(&buf, binary.BigEndian, uint16(1))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))

	//кодируем домен лейблами (длина + символы)
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("invalid domain label")
		}
		buf.WriteByte(byte(len(label)))
		buf.WriteString(label)
	}
	buf.WriteByte(0x00) //конец имени

	binary.Write(&buf, binary.BigEndian, uint16(1)) //QTYPE = A
	binary.Write(&buf, binary.BigEndian, uint16(1)) //QCLASS = IN

	return buf.Bytes(), nil
}

//парсим ответ и вытаскиваем первый IPv4 адрес
func parseDNSResponse(pkt []byte) (uint16, [4]byte, error) {
	var ip [4]byte
	if len(pkt) < 12 {
		return 0, ip, errors.New("packet too short")
	}
	id := binary.BigEndian.Uint16(pkt[0:2])
	flags := binary.BigEndian.Uint16(pkt[2:4])
	rcode := flags & 0x000F
	if rcode != 0 {
		return id, ip, fmt.Errorf("dns error rcode: %d", rcode)
	}

	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))

	offset := 12
	//пропускаем секцию вопросов
	for i := 0; i < qdCount; i++ {
		next, err := skipDNSName(pkt, offset)
		if err != nil || next+4 > len(pkt) {
			return id, ip, errors.New("malformed question")
		}
		offset = next + 4
	}

	//ищем первую A-запись в ответах
	for i := 0; i < anCount; i++ {
		next, err := skipDNSName(pkt, offset)
		if err != nil || next+10 > len(pkt) {
			return id, ip, errors.New("malformed answer")
		}
		qtype := binary.BigEndian.Uint16(pkt[next : next+2])
		qclass := binary.BigEndian.Uint16(pkt[next+2 : next+4])
		rdlength := int(binary.BigEndian.Uint16(pkt[next+8 : next+10]))
		rdataOffset := next + 10

		if rdataOffset+rdlength > len(pkt) {
			return id, ip, errors.New("truncated rdata")
		}

		if qtype == 1 && qclass == 1 && rdlength == 4 { //A-запись (IPv4)
			copy(ip[:], pkt[rdataOffset:rdataOffset+4])
			return id, ip, nil
		}
		offset = rdataOffset + rdlength
	}

	return id, ip, errors.New("no A record found")
}

//пропускаем доменное имя в DNS-пакете (с учетом сжатия через указатели 0xC0)
func skipDNSName(pkt []byte, offset int) (int, error) {
	for {
		if offset >= len(pkt) {
			return 0, errors.New("out of bounds")
		}
		length := int(pkt[offset])
		if length == 0 {
			return offset + 1, nil
		}
		if (length & 0xC0) == 0xC0 { //указатель сжатия
			if offset+2 > len(pkt) {
				return 0, errors.New("invalid pointer")
			}
			return offset + 2, nil
		}
		offset += 1 + length
	}
}