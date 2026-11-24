package main

import (
	"fmt"
	"os"
)

func main() {
	geo := os.Getenv("GEOAPIFY_KEY")
	owm := os.Getenv("OPENWEATHER_KEY")
	if geo == "" || owm == "" {
		fmt.Println("Задайте GEOAPIFY_KEY, OPENWEATHER_KEY")
		os.Exit(1)
	}
	runGUI(geo, owm)
}