package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func runLoop(rcv, snd *net.UDPConn, meIP, myID string, dst *net.UDPAddr, cfg Config, stop <-chan struct{}) {
	peers := NewPeerTable(cfg.MaxPeers, cfg.DeadTimeout)

	fmt.Println("Слушаю группу. Ctrl+C — выход.")
	peers.Print()
	fmt.Println()

	lastAnnounce := time.Now().Add(-cfg.AnnounceSec)
	lastSweep := time.Now()

	buf := make([]byte, 256)

	for {
		rcv.SetReadDeadline(time.Now().Add(1 * time.Second)) //прерываем через секунду
		n, addr, err := rcv.ReadFromUDP(buf)

		select { // проверка сигналов
		case <-stop:
			return
		default:
		}

		now := time.Now()

		if now.Sub(lastAnnounce) >= cfg.AnnounceSec { //отправляем анонс
			pkt := Packet{ID: myID, IP: meIP}
			if _, werr := snd.WriteToUDP(pkt.Marshal(), dst); werr != nil {
				fmt.Fprintf(os.Stderr, "announce: %v\n", werr)
			} else {
				lastAnnounce = now
			}
		}

		if now.Sub(lastSweep) >= cfg.SweepSec { //чистим мертвых
			dead := peers.RemoveDead(now)
			if len(dead) > 0 {
				for _, p := range dead {
					fmt.Printf("  [-] копия исчезла: %s\n", p.ip)
				}
				peers.Print()
			}
			lastSweep = now
		}

		if err != nil || n == 0 {
			continue
		}

		pkt, perr := ParsePacket(buf[:n])
		if perr != nil {
			continue // мусор/чужой протокол — молча игнорируем
		}
		if pkt.ID == myID { //игнор своих пакетов
			continue
		}
		senderIP := addr.IP.String()
		if peers.AddOrUpdate(pkt.ID, senderIP, now) {
			fmt.Printf("  [+] появилась новая копия: %s (id=%s)\n", senderIP, pkt.ID)
			peers.Print()
		}
	}
}
