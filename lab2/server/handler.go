package main

import (
	"log"
	"net"
)

// обслуживает одно TCP-соединение, посылает статус клиенту, логирует результат
func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()
	clientID := conn.RemoteAddr().String()
	log.Printf("[%s] connected", clientID)

	received, expected, err := receiveFile(conn, clientID, s.UploadsDir) 
	if err != nil { 
		log.Printf("[%s] ERROR: %v", clientID, err)
		_ = sendStatus(conn, statusFail)
		return
	}
	if received != expected {
		log.Printf("[%s] size mismatch: got %d, expected %d", clientID, received, expected)
		_ = sendStatus(conn, statusFail)
		return
	}
	_ = sendStatus(conn, statusOK)
	const mb = 1_000_000
	log.Printf("[%s] DONE, %.2f MB received", clientID, float64(received)/mb)
}