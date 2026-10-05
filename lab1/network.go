package main

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// определяет локальный IP, который будет использован для
// отправки в multicast-группу, а также резолвит адрес назначения.
func getMyAddr(family, mcast string, port int, iface *net.Interface) (string, *net.UDPAddr, error) {
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
	if family == "udp6" && dstAddr.IP.IsLinkLocalMulticast() && iface != nil {
		dstAddr.Zone = iface.Name
	}
	return myIP, dstAddr, nil
}

// создаёт UDP-сокет, привязанный к нужному семейству,
// и подписывает его на multicast-группу.
func CreateRecvSocket(family, mcast string, port int, iface *net.Interface) (*net.UDPConn, error) {
	if iface == nil {
		return nil, fmt.Errorf("no suitable interface")
	}
	conn, err := net.ListenUDP(family, &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

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

// создаёт UDP-сокет для отправки в multicast и
// устанавливает TTL/hop limit равным 1.
func createSendSocket(family string, iface *net.Interface) (*net.UDPConn, error) {
	conn, err := net.ListenUDP(family, nil)
	if err != nil {
		return nil, fmt.Errorf("listen send: %w", err)
	}

	if family == "udp4" {
		pc := ipv4.NewPacketConn(conn)
		if err := pc.SetMulticastTTL(1); err != nil {
			conn.Close()
			return nil, fmt.Errorf("SetMulticastTTL: %w", err)
		}
		if iface != nil {
			if err := pc.SetMulticastInterface(iface); err != nil {
				conn.Close()
				return nil, fmt.Errorf("SetMulticastInterface: %w", err)
			}
		}
	} else {
		pc := ipv6.NewPacketConn(conn)
		if err := pc.SetMulticastHopLimit(1); err != nil {
			conn.Close()
			return nil, fmt.Errorf("SetMulticastHopLimit: %w", err)
		}
		if iface != nil {
			if err := pc.SetMulticastInterface(iface); err != nil {
				conn.Close()
				return nil, fmt.Errorf("SetMulticastInterface: %w", err)
			}
		}
	}
	return conn, nil
}

// возвращает первый поднятый не-loopback интерфейс.
func findDefaultIface(family string) *net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if family == "udp4" && ipnet.IP.To4() != nil {
				return iface
			}
			if family == "udp6" && ipnet.IP.To4() == nil && ipnet.IP.IsGlobalUnicast() {
				return iface
			}
		}
	}
	return nil
}