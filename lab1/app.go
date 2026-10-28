package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func makeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
func Run(args []string) int {
	cfg := DefaultConfig()

	family, mcast, err := parseArg(args, cfg.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	iface := findDefaultIface(family)
	if iface == nil {
		fmt.Fprintln(os.Stderr, "нет подходящего интерфейса для", family)
		return 1
	}

	meIP, dst, err := getMyAddr(family, mcast, cfg.Port, iface)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get my addr:", err)
		return 1
	}

	myID := makeID()

	fmt.Printf("Протокол: %s, группа: %s, порт: %d\n",
		family, mcast, cfg.Port)
	fmt.Printf("Интерфейс: %s\n", iface.Name)
	fmt.Printf("Мой IP: %s\n", meIP)
	fmt.Printf("Мой ID: %s\n\n", myID)

	rcv, err := CreateRecvSocket(family, mcast, cfg.Port, iface)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recv socket:", err)
		return 1
	}
	defer rcv.Close()

	snd, err := createSendSocket(family, iface)
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

	runLoop(rcv, snd, meIP, myID, dst, cfg, stop)

	fmt.Println("\nЗавершение работы")
	return 0
}
