package main

import (
	"encoding/binary" 
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

type Client struct {
	Host string
	Port string
	Path string
}

func (c *Client) Run() error {
	f, size, nameBytes, err := openAndStat(c.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	//подключение к серверу
	addr := net.JoinHostPort(c.Host, c.Port)
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	// 13 тайм-ауты передачи данных и ожидания ответа
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	//отправка
	if err := sendFile(conn, f, size, nameBytes); err != nil {
		return err
	}

	if err := readStatus(conn); err != nil {
		// 15 просто возвращаем ошибку, чтобы сработали defer f.Close() и defer conn.Close()
		return err
	}

	fmt.Println("SUCCESS: file transferred and verified")
	return nil
}

func sendFile(conn net.Conn, f *os.File, size int64, nameBytes []byte) error {
	// 12 проверка полноты отправки заголовка
	if err := writeAll(conn, encodeUint32(uint32(len(nameBytes)))); err != nil {
		return fmt.Errorf("write name length: %w", err)
	}
	//имя
	if err := writeAll(conn, nameBytes); err != nil {
		return fmt.Errorf("write name: %w", err)
	}
	//размер
	if err := writeAll(conn, encodeUint64(uint64(size))); err != nil {
		return fmt.Errorf("write size: %w", err)
	}

	start := time.Now()
	n, err := io.Copy(conn, f) //содержимое
	if err != nil {
		return fmt.Errorf("send body (sent %d of %d): %w", n, size, err)
	}
	if n != size {
		return fmt.Errorf("short send: %d != %d", n, size)
	}
	elapsed := time.Since(start)
	const mb = 1_000_000
	fmt.Printf("sent %.2f MB in %s (%.2f MB/s)\n", float64(n)/mb, elapsed, float64(n)/mb/elapsed.Seconds())
	return nil
}

// 12 гарантированная запись всех байт
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func encodeUint32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func encodeUint64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}