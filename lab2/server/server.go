package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

type Server struct {
	Port string 
	UploadsDir string 
}

// создаёт каталог uploads, слушает порт и принимает соединения
func (s *Server) Run() error {
	const maxClients = 16

	if err := os.MkdirAll(s.UploadsDir, 0o755); err != nil { //создание каталогов
		return fmt.Errorf("create uploads dir: %w", err)
	}

	ln, err := net.Listen("tcp", ":"+s.Port)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()
	log.Printf("server listening on :%s, saving to %q", s.Port, s.UploadsDir)

	var wg sync.WaitGroup
	defer wg.Wait() 

	sem := make(chan struct{}, maxClients)

	for {    //горутины для разных клиентов 
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer func() { <-sem }()
			s.handleClient(c)
		}(conn)
	}
}