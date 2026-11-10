package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8" // 7
)

const (
	maxNameLen = 4096
	maxFileSize = int64(1) << 40 // 1 TiB
)

//открывает файл, проверяет, что это не каталог и что размер норм, возвращает открытый файл, его размер и UTF-8 байты имени
func openAndStat(path string) (*os.File, int64, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("open file: %w", err)
	}

	st, err := f.Stat() //получение методанных
	if err != nil {
		f.Close()
		return nil, 0, nil, fmt.Errorf("stat: %w", err)
	}
	if st.IsDir() { //тип
		f.Close()
		return nil, 0, nil, fmt.Errorf("%s is a directory", path)
	}

	size := st.Size()
	if size > maxFileSize {
		f.Close()
		return nil, 0, nil, fmt.Errorf("file too large: %d", size)
	}

	name := filepath.Base(path)
	nameBytes := []byte(name)

	// 7 проверка корректности UTF-8
	if !utf8.Valid(nameBytes) {
		f.Close()
		return nil, 0, nil, fmt.Errorf("invalid UTF-8 in filename")
	}

	if len(nameBytes) == 0 || len(nameBytes) > maxNameLen {
		f.Close()
		return nil, 0, nil, fmt.Errorf("invalid file name length: %d", len(nameBytes))
	}

	return f, size, nameBytes, nil
}