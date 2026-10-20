package main

import (
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

	//отправка
	if err := sendFile(conn, f, size, nameBytes); err != nil {
		return err
	}

	if err := readStatus(conn); err != nil {
		return err
	}

	fmt.Println("SUCCESS: file transferred and verified")
	return nil
}

func sendFile(conn net.Conn, f *os.File, size int64, nameBytes []byte) error {
	// длина имени
	if err := writeUint32(conn, uint32(len(nameBytes))); err != nil {
		return fmt.Errorf("write name length: %w", err)
	}
	//имя
	if _, err := conn.Write(nameBytes); err != nil {
		return fmt.Errorf("write name: %w", err)
	}
	//размер
	if err := writeUint64(conn, uint64(size)); err != nil {
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