package main

//фиксированный размер буфера на одно направление (чтобы не жрать память)
const bufSize = 32 * 1024 //8 требование

type Buffer struct {
	data [bufSize]byte
	r, w int
}

//сколько байт сейчас лежит в буфере
func (b *Buffer) Len() int {
	return b.w - b.r
}

//сколько места осталось для записи
func (b *Buffer) Avail() int {
	return len(b.data) - b.w
}

//пишем данные в буфер, при необходимости сдвигаем непрочитанный хвост в начало
func (b *Buffer) Write(p []byte) int {
	if b.r == b.w {
		b.r, b.w = 0, 0
	} else if b.r > 0 && b.Avail() < len(p) {
		copy(b.data[:], b.data[b.r:b.w])
		b.w -= b.r
		b.r = 0
	}
	n := copy(b.data[b.w:], p)
	b.w += n
	return n
}

//срез непрочитанных данных
func (b *Buffer) Bytes() []byte {
	return b.data[b.r:b.w]
}

//сдвигаем указатель чтения после отправки
func (b *Buffer) Consume(n int) {
	b.r += n
	if b.r == b.w {
		b.r, b.w = 0, 0
	}
}