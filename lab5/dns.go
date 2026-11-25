package main
import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"golang.org/x/sys/unix"
)

//таймаут и лимит попыток днс-запроса, 10 требование
const (
	dnsQueryTimeout = 2 * time.Second
	dnsMaxAttempts = 3
	dnsTypeA = 1
	dnsTypeCNAME = 5
	dnsClassIN = 1
)

type DNSResolver struct {
	fd int
	serverAddr [4]byte
	serverPort int
	serverSA *unix.SockaddrInet4
	pending map[uint16]*Session
	recvBuf [2048]byte
}

//создаем один неблокирующий юдп-сокет для всех днс-запросов, 2 и 10 требование
func newDNSResolver(customIP net.IP, customPort int) (*DNSResolver, error) {
	var ip4 net.IP
	port := 53

	if customIP != nil {
		ip4 = customIP.To4()
		port = customPort
	} else {
		dnsIP := getSystemDNS()
		ip4 = dnsIP.To4()
		if ip4 == nil {
			ip4 = net.IPv4(8, 8, 8, 8).To4()
		}
	}

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_UDP) //неблок днс сокет
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil { //5 требование
		unix.Close(fd)
		return nil, err
	}

	var addr [4]byte
	copy(addr[:], ip4)
	sa := &unix.SockaddrInet4{Port: port, Addr: addr}
	return &DNSResolver{
		fd: fd,
		serverAddr: addr,
		serverPort: port,
		serverSA: sa,
		pending: make(map[uint16]*Session),
	}, nil
}

//берем адрес резолвера из /etc/resolv.conf 
func getSystemDNS() net.IP {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return net.IPv4(8, 8, 8, 8)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			ip := net.ParseIP(fields[1])
			if ip != nil && ip.To4() != nil {
				return ip.To4()
			}
		}
	}
	return net.IPv4(8, 8, 8, 8)
}

//отправляем днс A-запрос и запоминаем сессию, 10 требование
func (r *DNSResolver) Resolve(domain string, sess *Session) error {
	clean := normalizeDomain(domain) //очищ имя 
	if clean == "" {
		return errors.New("empty domain")
	}
	sess.dnsDomain = clean
	sess.dnsAttempts = 0
	return r.sendAttempt(sess)
}

//отправляем или повторяем днс-запрос со случайным ID, 10 требование
func (r *DNSResolver) sendAttempt(sess *Session) error {
	if sess.dnsID != 0 {
		delete(r.pending, sess.dnsID)
		sess.dnsID = 0
	}
	id, err := r.allocateRandomID()
	if err != nil {
		return err
	}
	pkt, err := buildDNSQuery(id, sess.dnsDomain) //собираем бин пакет
	if err != nil {
		return err
	}

	for {
		err = unix.Sendto(r.fd, pkt, 0, r.serverSA) //неблок вызов, отправляет днс запрос на днс сервер
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			break
		}
		return err
	}
	sess.dnsID = id
	sess.dnsAttempts++
	sess.dnsSentAt = time.Now()
	r.pending[id] = sess
	return nil
}

//генерирует случайный id пакета
func (r *DNSResolver) allocateRandomID() (uint16, error) {
	var b [2]byte
	for i := 0; i < 1024; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint16(b[:])
		if id == 0 {
			continue
		}
		if _, busy := r.pending[id]; !busy {
			return id, nil
		}
	}
	return 0, errors.New("dns query queue full")
}

//отменяет ожидание днс при закрытии сессии
func (r *DNSResolver) Cancel(sess *Session) {
	if sess.dnsID != 0 {
		delete(r.pending, sess.dnsID)
		sess.dnsID = 0
	}
}

func normalizeDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

//собирает днс A-запрос по RFC 1035
func buildDNSQuery(id uint16, domain string) ([]byte, error) {
	domain = normalizeDomain(domain)

	if len(domain) == 0 || len(domain) > 253 { //проверка длины домена
		return nil, errors.New("invalid domain length")
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, id)
	binary.Write(&buf, binary.BigEndian, uint16(0x0100))
	binary.Write(&buf, binary.BigEndian, uint16(1))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("invalid domain label")
		}
		buf.WriteByte(byte(len(label)))
		buf.WriteString(label)
	}
	buf.WriteByte(0x00)
	binary.Write(&buf, binary.BigEndian, uint16(dnsTypeA))
	binary.Write(&buf, binary.BigEndian, uint16(dnsClassIN))
	return buf.Bytes(), nil
}

//достает id из заголовка пакета
func extractDNSID(pkt []byte) (uint16, bool) {
	if len(pkt) < 12 {
		return 0, false
	}
	return binary.BigEndian.Uint16(pkt[0:2]), true
}

//разбирает днс-ответ
func parseDNSResponse(pkt []byte, expectedDomain string) ([4]byte, error) {
	var ip [4]byte
	if len(pkt) < 12 {
		return ip, errors.New("packet too short")
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if (flags & 0x8000) == 0 {       //проверяет бит QR, если 0 - запрос а не ответ
		return ip, errors.New("not a DNS response (QR=0)")
	}
	if ((flags >> 11) & 0x0F) != 0 { 
		return ip, errors.New("unexpected DNS opcode")
	}
	rcode := flags & 0x000F
	if rcode != 0 {
		return ip, fmt.Errorf("dns error rcode: %d", rcode)
	}
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if qdCount != 1 {
		return ip, errors.New("unexpected question count")
	}
	offset := 12

	//сверяем секцию вопроса с тем что запрашивали
	qName, next, err := decodeDNSName(pkt, offset)
	if err != nil || next+4 > len(pkt) {
		return ip, errors.New("malformed question")
	}
	qType := binary.BigEndian.Uint16(pkt[next : next+2])
	qClass := binary.BigEndian.Uint16(pkt[next+2 : next+4])
	offset = next + 4
	expectedNorm := normalizeDomain(expectedDomain)
	//проверяем что норм
	if normalizeDomain(qName) != expectedNorm || qType != dnsTypeA || qClass != dnsClassIN {
		return ip, errors.New("question mismatch")
	}
	
	//разбираем секцию ответов
	allowedNames := map[string]bool{
		expectedNorm: true,
	}
	for i := 0; i < anCount; i++ {
		rrName, rrNext, err := decodeDNSName(pkt, offset)
		if err != nil || rrNext+10 > len(pkt) {
			return ip, errors.New("malformed answer record")
		}

		rrType := binary.BigEndian.Uint16(pkt[rrNext : rrNext+2])
		rrClass := binary.BigEndian.Uint16(pkt[rrNext+2 : rrNext+4])
		rdlength := int(binary.BigEndian.Uint16(pkt[rrNext+8 : rrNext+10]))
		rdataOffset := rrNext + 10
		if rdataOffset+rdlength > len(pkt) {
			return ip, errors.New("truncated rdata")
		}
		ownerNorm := normalizeDomain(rrName)
		if rrClass == dnsClassIN && allowedNames[ownerNorm] {
			switch rrType {
			case dnsTypeCNAME:
				cnameTarget, cnameEnd, err := decodeDNSName(pkt, rdataOffset)
				if err != nil || cnameEnd != rdataOffset+rdlength {
					return ip, errors.New("malformed CNAME rdata")
				}
				targetNorm := normalizeDomain(cnameTarget)
				if targetNorm != "" {
					allowedNames[targetNorm] = true
				}
			case dnsTypeA:
				if rdlength != 4 {
					return ip, errors.New("invalid A record length")
				}
				copy(ip[:], pkt[rdataOffset:rdataOffset+4])
				return ip, nil
			}
		}
		offset = rdataOffset + rdlength
	}
	if (flags & 0x0200) != 0 {
		return ip, errors.New("truncated DNS response (TC=1)")
	}
	return ip, errors.New("no matching A record found")
}

//читает доменное имя по формату сжатия RFC 1035
func decodeDNSName(pkt []byte, offset int) (string, int, error) {
	var labels []string
	cur := offset
	nextOffset := -1
	jumped := false
	jumps := 0
	for {
		if cur >= len(pkt) {
			return "", 0, errors.New("out of bounds")
		}
		length := int(pkt[cur])
		if length == 0 {
			if !jumped {
				nextOffset = cur + 1
			}
			break
		}
		if (length & 0xC0) == 0xC0 {
			if cur+2 > len(pkt) {
				return "", 0, errors.New("invalid pointer")
			}
			ptr := int(binary.BigEndian.Uint16(pkt[cur:cur+2]) & 0x3FFF)
			if ptr >= len(pkt) {
				return "", 0, errors.New("pointer out of bounds")
			}
			if !jumped {
				nextOffset = cur + 2
				jumped = true
			}
			cur = ptr
			jumps++
			if jumps > 64 {
				return "", 0, errors.New("pointer loop")
			}
			continue
		}
		if (length & 0xC0) != 0 {
			return "", 0, errors.New("invalid label prefix")
		}
		if cur+1+length > len(pkt) {
			return "", 0, errors.New("label out of bounds")
		}
		labels = append(labels, string(pkt[cur+1:cur+1+length]))
		cur += 1 + length
	}
	return strings.Join(labels, "."), nextOffset, nil
}