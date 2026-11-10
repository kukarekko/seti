package main

import (
	"encoding/binary"
	"io"
)

const (
	maxNameLen = 4096
	maxFileSize = uint64(1) << 40 // 1 TiB 

	statusOK = byte(1)
	statusFail = byte(0)
)

func readUint32(r io.Reader) (uint32, error) { //4-байтовое
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func readUint64(r io.Reader) (uint64, error) { //8-байтовое
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

func sendStatus(w io.Writer, code byte) error {
	_, err := w.Write([]byte{code})
	return err
}