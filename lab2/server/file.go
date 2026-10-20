package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// прием файла по протоколу с подсчетом скорости
func receiveFile(conn net.Conn, clientID, uploadsDir string) (int64, int64, error) { // читает протокол, пишет файл, запускает горутину-репортёр.
	
	nameLen, err := readUint32(conn)
	if err != nil {
		return 0, 0, fmt.Errorf("read name length: %w", err)
	}
	if nameLen == 0 || nameLen > maxNameLen {
		return 0, 0, fmt.Errorf("bad name length: %d", nameLen)
	} 

	//чтение имени файла
	nameBytes := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, nameBytes); err != nil {
		return 0, 0, fmt.Errorf("read name: %w", err)
	}
	rawName := string(nameBytes)

	//размер
	sizeU, err := readUint64(conn)
	if err != nil {
		return 0, 0, fmt.Errorf("read size: %w", err)
	}
	if sizeU > maxFileSize {
		return 0, 0, fmt.Errorf("file too large: %d", sizeU)
	}
	size := int64(sizeU)

	safeName, err := safeFilename(rawName) //норм имя
	if err != nil {
		return 0, 0, fmt.Errorf("unsafe filename %q: %w", rawName, err)
	}

	dstPath := filepath.Join(uploadsDir, safeName) //склеивает компоненты пути
	dst, err := os.Create(dstPath) //создает файл
	if err != nil {
		return 0, 0, fmt.Errorf("create %s: %w", dstPath, err)
	}
	committed := false
	defer func() {
		dst.Close()
		if !committed {
			_ = os.Remove(dstPath)
		}
	}()

	stats := &speedStats{sessionStart: time.Now()} 
	
	stop := make(chan struct{})
	reportDone := make(chan struct{}) //канал для обратного сигнала
	go reportLoop(clientID, stats, stop, reportDone)

	var received int64
	buf := make([]byte, 64*1024)
	lastTick := stats.sessionStart
	var lastBytes int64

	for received < size { // пока принято меньше чем заявлено
		toRead := int64(len(buf))
		if remaining := size - received; remaining < toRead { 
			toRead = remaining
		}
		n, err := conn.Read(buf[:toRead]) //читает
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				close(stop)
				<-reportDone
				return received, size, fmt.Errorf("write: %w", werr)
			}
			received += int64(n) // обновление счетчика принятых байт

			now := time.Now()
			stats.mu.Lock()
			stats.total = received
			stats.mu.Unlock()

			if now.Sub(lastTick) >= time.Second { 
				stats.mu.Lock()
				stats.lastBytes = received - lastBytes
				stats.lastTick = now
				stats.mu.Unlock()
				lastTick = now
				lastBytes = received
			}
		}
		if err != nil {
			close(stop)
			<-reportDone
			if errors.Is(err, io.EOF) {
				return received, size, fmt.Errorf("unexpected EOF at %d/%d", received, size)
			}
			return received, size, fmt.Errorf("read: %w", err)
		}
	}

	close(stop)
	<-reportDone

	if err := dst.Sync(); err != nil { //запись на диск
		return received, size, fmt.Errorf("sync: %w", err)
	}
	committed = true 
	return received, size, nil
}

//5 треб 
func safeFilename(raw string) (string, error) { 
	normalized := strings.ReplaceAll(raw, "\\", "/")
	base := filepath.Base(normalized) 

	if base == "." || base == ".." || base == "/" || base == "" {
		return "", errors.New("empty or traversal name")
	}
	if strings.ContainsAny(base, `/\`) {
		return "", errors.New("contains path separator")
	}
	for _, r := range base { // проверка управляющих
		if r < 0x20 {
			return "", errors.New("control char in name")
		}
	}
	return base, nil
}