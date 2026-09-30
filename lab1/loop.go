package main

import (
	"fmt"
	"net"
	"time"
)

func runLoop(rcv, snd *net.UDPConn, meIP string, dst *net.UDPAddr, cfg Config, stop <-chan struct{}) {
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
			snd.WriteToUDP([]byte(meIP), dst)
			lastAnnounce = now
		}

		if now.Sub(lastSweep) >= cfg.SweepSec { //чистим мертвых
			dead := peers.RemoveDead(now)
			if len(dead) > 0 {
				for _, ip := range dead {
					fmt.Printf("  [-] копия исчезла: %s\n", ip)
				}
				peers.Print()
			}
			lastSweep = now
		}

		if err == nil && n > 0 {
			sender := addr.IP.String()
			if sender == meIP { //игнор своих пакетов
				continue
			}
			if peers.AddOrUpdate(sender, now) {
				fmt.Printf("  [+] появилась новая копия: %s\n", sender)
				peers.Print()
			}
		}
	}
}