package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func Run(args []string) int {
	cfg := DefaultConfig()

	family, mcast, err := parseArg(args, cfg.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	meIP, dst, err := getMyAddr(family, mcast, cfg.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get my addr:", err)
		return 1
	}

	fmt.Printf("Протокол: %s, группа: %s, порт: %d\n",
		family, mcast, cfg.Port)
	fmt.Printf("Мой IP: %s\n\n", meIP)

	rcv, err := CreateRecvSocket(family, mcast, cfg.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recv socket:", err)
		return 1
	}
	defer rcv.Close()

	snd, err := createSendSocket(family)
	if err != nil {
		fmt.Fprintln(os.Stderr, "send socket:", err)
		return 1
	}
	defer snd.Close()

	stop := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		close(stop)
	}()

	runLoop(rcv, snd, meIP, dst, cfg, stop)

	fmt.Println("\nЗавершение работы")
	return 0
}