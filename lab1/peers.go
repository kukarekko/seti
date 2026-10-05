package main

import (
	"fmt"
	"sync"
	"time"
)

type peerInfo struct {
    ip       string
    lastSeen time.Time
}

type PeerTable struct {
	mu       sync.Mutex
	peers    map[string]peerInfo 
	maxPeers int
	timeout  time.Duration
}

func NewPeerTable(maxPeers int, timeout time.Duration) *PeerTable {
	return &PeerTable{
		peers:    make(map[string]peerInfo),
		maxPeers: maxPeers,
		timeout:  timeout,
	}
}

func (t *PeerTable) AddOrUpdate(id, ip string, now time.Time) bool {
    t.mu.Lock()
    defer t.mu.Unlock()

    if p, exists := t.peers[id]; exists {
        p.lastSeen = now
        p.ip = ip
        t.peers[id] = p
        return false
    }
    if len(t.peers) >= t.maxPeers {
        return false
    }
    t.peers[id] = peerInfo{ip: ip, lastSeen: now}
    return true
}

// удаляет пиров, не подававших признаков жизни дольше timeout.
func (t *PeerTable) RemoveDead(now time.Time) []peerInfo {
	t.mu.Lock()
	defer t.mu.Unlock()

	var dead []peerInfo
	for id, p := range t.peers {
		if now.Sub(p.lastSeen) > t.timeout {
			dead = append(dead, p)
			delete(t.peers, id)
		}
	}
	return dead
}

func (t *PeerTable) Print() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	fmt.Printf("Живых копий: %d\n", len(t.peers))
	for id, p := range t.peers {
    	fmt.Printf("    %s  id=%s  (последний раз: %d c назад)\n",
        p.ip, id, int(now.Sub(p.lastSeen).Seconds()))
	}
}