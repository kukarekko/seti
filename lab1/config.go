package main

import "time"

type Config struct {
	Port        int
	AnnounceSec time.Duration
	DeadTimeout time.Duration
	SweepSec    time.Duration
	MaxPeers    int
}

func DefaultConfig() Config {
	return Config{
		Port:        45678,
		AnnounceSec: 2 * time.Second,
		DeadTimeout: 5 * time.Second,
		SweepSec:    1 * time.Second,
		MaxPeers:    64,
	}
}