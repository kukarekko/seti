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

	// 5 треб: принимаем имя файла из receiveFile
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
	_ = sendStatus(conn, statusOK)
	const mb = 1_000_000
	// 5 треб: пишем, какой именно файл завершил отправку
	log.Printf("[%s] DONE: file '%s' successfully received, %.2f MB", clientID, filename, float64(received)/mb)
}