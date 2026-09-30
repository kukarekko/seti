package main

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// getMyAddr определяет локальный IP, который будет использован для
// отправки в multicast-группу, а также резолвит адрес назначения.
func getMyAddr(family, mcast string, port int) (string, *net.UDPAddr, error) {
	conn, err := net.Dial(family, net.JoinHostPort(mcast, strconv.Itoa(port)))
	if err != nil {
		return "", nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	myIP := conn.LocalAddr().(*net.UDPAddr).IP.String()

	dstAddr, err := net.ResolveUDPAddr(family, net.JoinHostPort(mcast, strconv.Itoa(port)))
	if err != nil {
		return "", nil, fmt.Errorf("resolve dst: %w", err)
	}
	return myIP, dstAddr, nil
}

// CreateRecvSocket создаёт UDP-сокет, привязанный к нужному семейству,
// и подписывает его на multicast-группу.
func CreateRecvSocket(family, mcast string, port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP(family, &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	iface := findDefaultIface()
	group := &net.UDPAddr{IP: net.ParseIP(mcast)}

	if family == "udp4" {
		if err := ipv4.NewPacketConn(conn).JoinGroup(iface, group); err != nil {
			conn.Close()
			return nil, fmt.Errorf("JoinGroup IPv4: %w", err)
		}
	} else {
		if err := ipv6.NewPacketConn(conn).JoinGroup(iface, group); err != nil {
			conn.Close()
			return nil, fmt.Errorf("JoinGroup IPv6: %w", err)
		}
	}
	return conn, nil
}

// createSendSocket создаёт UDP-сокет для отправки в multicast и
// устанавливает TTL/hop limit равным 1 (в пределах одного сегмента).
func createSendSocket(family string) (*net.UDPConn, error) {
	conn, err := net.ListenUDP(family, nil)
	if err != nil {
		return nil, fmt.Errorf("listen send: %w", err)
	}

	if family == "udp4" {
		if err := ipv4.NewPacketConn(conn).SetMulticastTTL(1); err != nil {
			conn.Close()
			return nil, fmt.Errorf("SetMulticastTTL: %w", err)
		}
	} else {
		if err := ipv6.NewPacketConn(conn).SetMulticastHopLimit(1); err != nil {
			conn.Close()
			return nil, fmt.Errorf("SetMulticastHopLimit: %w", err)
		}
	}
	return conn, nil
}

// findDefaultIface возвращает первый поднятый не-loopback интерфейс.
func findDefaultIface() *net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			return iface
		}
	}
	return nil
}