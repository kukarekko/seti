package main

import (
	"errors" 
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: %s <host> <port> <file>\n", os.Args[0])
		os.Exit(1)
	} 
	c := &Client{   
		Host: os.Args[1],
		Port: os.Args[2],
		Path: os.Args[3],
	}  
	
	if err := c.Run(); err != nil {
		// 15 перехватываем отказ сервера и завершаем с кодом 2
		if errors.Is(err, ErrServerRejected) { 
			fmt.Fprintln(os.Stderr, "FAILURE: server reported error")
			os.Exit(2)
		}
		
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}
}