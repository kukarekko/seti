package main

import (
	"log"
	"sync"
	"time"
)

const reportEvery = 3 * time.Second

type speedStats struct {
	mu           sync.Mutex
	total        int64
	lastBytes    int64
	lastTick     time.Time
	sessionStart time.Time
}

func reportLoop(clientID string, s *speedStats, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(reportEvery)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// 9 при завершении всегда печатаем финальный отчёт
			printReport(clientID, s)
			return
		case <-ticker.C:
			printReport(clientID, s)
		}
	}
}

func printReport(clientID string, s *speedStats) {
	s.mu.Lock()
	total := s.total
	last := s.lastBytes
	elapsed := time.Since(s.sessionStart).Seconds()
	// 8: длительность последнего окна
	window := 1.0
	if elapsed > 0 && !s.lastTick.IsZero() {
		window = s.lastTick.Sub(s.sessionStart).Seconds()
		if window < 1.0 {
			window = 1.0
		}
	}
	s.mu.Unlock()

	const mb = 1_000_000

	// 8 делим на фактическую длительность интервала
	instant := float64(last) / window / mb
	var avg float64
	if elapsed > 0 {
		avg = float64(total) / elapsed / mb
	}

	log.Printf("[%s] speed: instant=%.2f MB/s, average=%.2f MB/s, total=%.2f MB",
		clientID, instant, avg, float64(total)/mb)
}