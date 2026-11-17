package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
)

func main() {
	//привязываем к одному потоку ОС, чтобы не плодить треды
	runtime.LockOSThread()

	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <port>\n", os.Args[0])
		os.Exit(1)
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "Invalid port: %s\n", os.Args[1])
		os.Exit(1)
	}

	server, err := newProxyServer(port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start proxy: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("SOCKS5 proxy listening on port %d...\n", port)
	server.Run()
}