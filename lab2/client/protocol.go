package main

import (
	"encoding/binary"
	"errors" // 15 добавлен errors
	"fmt"
	"io"
)

const (
	statusOK = byte(1)
	statusFail = byte(0)
)

// 15 создаем sentinel-ошибку, чтобы main мог ее отловить
var ErrServerRejected = errors.New("server rejected the file")

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

//читает один байт статуса от сервера и превращает его в ошибку
func readStatus(r io.Reader) error {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return fmt.Errorf("read status: %w", err)
	}

	if b[0] == statusOK {
		return nil
	} else if b[0] == statusFail {
		// 15 возвращаем ошибку вместо os.Exit, чтобы сработали defer в Run()
		return ErrServerRejected 
	} else {
		return fmt.Errorf("unknown status: %d", b[0])
	}
}