package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
)

func main() {
	//привязываем к одному потоку ОС, чтобы не плодить треды
	runtime.LockOSThread() //5 требование

	//2 требование: 1-й аргумент обязательный (порт), 2-й необязательный (IPv4:port DNS-резолвера)
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <port> [dns_ipv4:port]\n", os.Args[0])
		os.Exit(1)
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "Invalid port: %s\n", os.Args[1])
		os.Exit(1)
	}

	//2 требование: парсим второй необязательный аргумент адрес DNS-резолвера
	var customDNSIP net.IP
	var customDNSPort int
	if len(os.Args) == 3 {
		host, portStr, err := net.SplitHostPort(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid DNS address (expected IPv4:port): %s\n", os.Args[2])
			os.Exit(1)
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			fmt.Fprintf(os.Stderr, "Invalid DNS IPv4: %s\n", host)
			os.Exit(1)
		}
		dPort, err := strconv.Atoi(portStr)
		if err != nil || dPort < 1 || dPort > 65535 {
			fmt.Fprintf(os.Stderr, "Invalid DNS port: %s\n", portStr)
			os.Exit(1)
		}
		customDNSIP = ip.To4()
		customDNSPort = dPort
	}

	server, err := newProxyServer(port, customDNSIP, customDNSPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start proxy: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("SOCKS5 proxy listening on port %d...\n", port)
	server.Run()
}