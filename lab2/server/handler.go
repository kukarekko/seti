package main

import (
	"log"
	"net"
	"time" // 10
)

// обслуживает одно TCP-соединение, посылает статус клиенту, логирует результат
func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()
	clientID := conn.RemoteAddr().String()
	log.Printf("[%s] connected", clientID)

	// 10 тайм-аут чтения
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	received, expected, filename, err := receiveFile(conn, clientID, s.UploadsDir) 
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
	
	// 10 тайм-аут записи
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	
	_ = sendStatus(conn, statusOK)
	const mb = 1_000_000
	log.Printf("[%s] DONE: file '%s' successfully received, %.2f MB", clientID, filename, float64(received)/mb)
}