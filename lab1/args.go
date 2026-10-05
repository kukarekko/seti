package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// parseArg проверяет аргумент командной строки и возвращает семейство
// сокета ("udp4"/"udp6") и адрес multicast-группы.
func parseArg(args []string, port int) (string, string, error) {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "Использование: %s <multicast-адрес>\n", args[0])
		fmt.Fprintln(os.Stderr, "  IPv4: 224.0.0.1")
		fmt.Fprintln(os.Stderr, "  IPv6: ff02::1")
		return "", "", fmt.Errorf("неверное число аргументов")
	}

	s := args[1]

	ip := net.ParseIP(s)
	if ip == nil {
		return "", "", fmt.Errorf("некорректный адрес группы: %s", s)
	}

	if ip.To4() != nil {
		firstOctet, err := strconv.Atoi(strings.Split(s, ".")[0])
		if err != nil || firstOctet < 224 || firstOctet > 239 {
			return "", "", fmt.Errorf("адрес %s не является IPv4 multicast", s)
		}
		return "udp4", s, nil
	}

	if !ip.IsMulticast() {
		return "", "", fmt.Errorf("адрес %s не является IPv6 multicast", s)
	}
	return "udp6", s, nil
}