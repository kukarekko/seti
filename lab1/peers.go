package main

import (
	"fmt"
	"sync"
	"time"
)

type PeerTable struct {
	mu       sync.Mutex
	peers    map[string]time.Time
	maxPeers int
	timeout  time.Duration
}

func NewPeerTable(maxPeers int, timeout time.Duration) *PeerTable {
	return &PeerTable{
		peers:    make(map[string]time.Time),
		maxPeers: maxPeers,
		timeout:  timeout,
	}
}

// AddOrUpdate возвращает true, если пир добавлен впервые.
func (t *PeerTable) AddOrUpdate(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.peers[ip]; exists {
		t.peers[ip] = now
		return false
	}
	if len(t.peers) >= t.maxPeers {
		return false
	}
	t.peers[ip] = now
	return true
}

// RemoveDead удаляет пиров, не подававших признаков жизни дольше timeout.
func (t *PeerTable) RemoveDead(now time.Time) []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var dead []string
	for ip, lastSeen := range t.peers {
		if now.Sub(lastSeen) > t.timeout {
			dead = append(dead, ip)
			delete(t.peers, ip)
		}
	}
	return dead
}

func (t *PeerTable) Print() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	fmt.Printf("Живых копий: %d\n", len(t.peers))
	for ip, lastSeen := range t.peers {
		fmt.Printf("    %s  (последний раз: %d c назад)\n",
			ip, int(now.Sub(lastSeen).Seconds()))
	}
}