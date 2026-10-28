package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	statusOK = byte(1)
	statusFail = byte(0)
)

//4-бфйтовые
func writeUint32(w io.Writer, v uint32) error { 
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v) //положить число v в байты b в порядке big-endian
	_, err := w.Write(b[:])
	return err
}

//8-байтовые
func writeUint64(w io.Writer, v uint64) error {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, err := w.Write(b[:])
	return err
}

//читает один байт статуса от сервера и превращает его в ошибку или в os.Exit(2) 
func readStatus(r io.Reader) error {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return fmt.Errorf("read status: %w", err)
	}

	if b[0] == statusOK {
		return nil
	} else if b[0] == statusFail {
		fmt.Println("FAILURE: server reported error")
		os.Exit(2)
		return nil
	} else {
		return fmt.Errorf("unknown status: %d", b[0])
	}

}