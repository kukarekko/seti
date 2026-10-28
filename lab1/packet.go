package main

import (
	"errors"
	"strings"
)

const (
	protoMagic   = "PING"
	protoVersion = 1
)

type Packet struct {
	ID string
	IP string
}

func (p Packet) Marshal() []byte {
	b := make([]byte, 0, 6+len(p.ID)+len(p.IP))
	b = append(b, protoMagic...)
	b = append(b, protoVersion)
	b = append(b, byte(len(p.ID)))
	b = append(b, p.ID...)
	b = append(b, p.IP...)
	return b
}

var errBadPacket = errors.New("bad packet")

func ParsePacket(b []byte) (Packet, error) {
	if len(b) < 6 || string(b[:4]) != protoMagic {
		return Packet{}, errBadPacket
	}
	if b[4] != protoVersion {
		return Packet{}, errBadPacket
	}
	idLen := int(b[5])
	if len(b) < 6+idLen {
		return Packet{}, errBadPacket
	}
	id := string(b[6 : 6+idLen])
	ip := string(b[6+idLen:])
	if id == "" || strings.ContainsRune(id, 0) {
		return Packet{}, errBadPacket
	}
	return Packet{ID: id, IP: ip}, nil
}
