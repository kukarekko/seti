package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync" 
	"time"
	"unicode/utf8" 
)

// 6  глобальные структуры для проверки занятости имён
var (
	namesInUse = make(map[string]bool)
	namesMu    sync.Mutex
)

// прием файла по протоколу с подсчетом скорости
func receiveFile(conn net.Conn, clientID, uploadsDir string) (int64, int64, string, error) { // читает протокол, пишет файл, запускает горутину-репортёр.
	
	nameLen, err := readUint32(conn)
	if err != nil {
		return 0, 0, "", fmt.Errorf("read name length: %w", err)
	}
	if nameLen == 0 || nameLen > maxNameLen {
		return 0, 0, "", fmt.Errorf("bad name length: %d", nameLen)
	} 

	//чтение имени файла
	nameBytes := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, nameBytes); err != nil {
		return 0, 0, "", fmt.Errorf("read name: %w", err)
	}
	rawName := string(nameBytes)

	// 7 проверка UTF-8
	if !utf8.ValidString(rawName) {
		return 0, 0, "", fmt.Errorf("invalid UTF-8 in filename")
	}

	//размер
	sizeU, err := readUint64(conn)
	if err != nil {
		return 0, 0, "", fmt.Errorf("read size: %w", err)
	}
	if sizeU > maxFileSize {
		return 0, 0, "", fmt.Errorf("file too large: %d", sizeU)
	}
	size := int64(sizeU)

	safeName, err := safeFilename(rawName) //норм имя
	if err != nil {
		return 0, 0, "", fmt.Errorf("unsafe filename %q: %w", rawName, err)
	}

	// 6 захват уникального имени
	uniqueName := claimName(safeName)

	dstPath := filepath.Join(uploadsDir, uniqueName) //склеивает компоненты пути
	dst, err := os.Create(dstPath) //создает файл
	if err != nil {
		releaseName(uniqueName) // 6
		return 0, 0, "", fmt.Errorf("create %s: %w", dstPath, err)
	}
	committed := false
	defer func() {
		// 11 если файл не был закрыт явно, закрываем здесь
		if !committed {
			dst.Close()
			_ = os.Remove(dstPath)
		}
		releaseName(uniqueName) // 6
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
		
		// 10 тайм-аут на каждой итерации
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		
		n, err := conn.Read(buf[:toRead]) //читает
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				close(stop)
				<-reportDone
				return received, size, uniqueName, fmt.Errorf("write: %w", werr)
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
				return received, size, uniqueName, fmt.Errorf("unexpected EOF at %d/%d", received, size)
			}
			return received, size, uniqueName, fmt.Errorf("read: %w", err)
		}
	}

	close(stop)
	<-reportDone

	if err := dst.Sync(); err != nil { //запись на диск
		return received, size, uniqueName, fmt.Errorf("sync: %w", err)
	}

	// 11: проверка успешности закрытия файла
	if err := dst.Close(); err != nil {
		return received, size, uniqueName, fmt.Errorf("close: %w", err)
	}
	
	committed = true 
	return received, size, uniqueName, nil
}

// 6 генерация уникального имени
func claimName(name string) string {
	namesMu.Lock()
	defer namesMu.Unlock()

	final := name
	for i := 1; namesInUse[final]; i++ {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		final = fmt.Sprintf("%s_%d%s", base, i, ext)
	}
	namesInUse[final] = true
	return final
}

// 6 освобождение имени
func releaseName(name string) {
	namesMu.Lock()
	delete(namesInUse, name)
	namesMu.Unlock()
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