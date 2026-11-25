package main
import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"golang.org/x/sys/unix"
)
func main() {
	runtime.LockOSThread() //5 требование, привязывает глав горутину к одному потоку
	signal.Ignore(unix.SIGPIPE)

	//1-й арг порт прокси, 2-й необяз порт DNS-резолвера (2 требование)
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <port> [dns_ipv4:port]\n", os.Args[0])
		os.Exit(1)
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "Invalid port: %s\n", os.Args[1])
		os.Exit(1)
	}

	//разбор адреса DNS-резолвера, 2 требование
	var customDNSIP net.IP
	var customDNSPort int
	if len(os.Args) == 3 {
		host, portStr, err := net.SplitHostPort(os.Args[2]) //разделяет на ip адрес и порт
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid DNS address (expected IPv4:port): %s\n", os.Args[2])
			os.Exit(1)
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			fmt.Fprintf(os.Stderr, "Invalid DNS IPv4: %s\n", host)
			os.Exit(1)
		}
		dPort, err := strconv.Atoi(portStr) //в число
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