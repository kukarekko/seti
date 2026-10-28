package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	gh, owm, otm := os.Getenv("GRAPHHOPPER_KEY"), os.Getenv("OPENWEATHER_KEY"), os.Getenv("OPENTRIPMAP_KEY") //чт ключей
	if gh == "" || owm == "" || otm == "" {
		fmt.Println("Задайте GRAPHHOPPER_KEY, OPENWEATHER_KEY, OPENTRIPMAP_KEY")
		os.Exit(1)
	}

	in := bufio.NewReader(os.Stdin) //чт запроса
	fmt.Print("Введите название для поиска: ")
	query, _ := in.ReadString('\n')
	query = strings.TrimSpace(query)
	if query == "" {
		fmt.Println("Пустой запрос")
		os.Exit(1)
	}

	// единственная блокировка - ожидание конечного результата
	rep, err := app(query, in, gh, owm, otm).Await()
	if err != nil {
		fmt.Println("Ошибка:", err)
		os.Exit(1)
	}

	w := rep.Weather
	fmt.Printf("\n=== %s ===\n", rep.Location.Name)
	fmt.Printf("Погода: %s, %.1f°C (ощущается %.1f°C), влажность %d%%, давление %d гПа, ветер %.1f м/с\n", w.Description, w.Temp, w.Feels, w.Humidity, w.Pressure, w.WindSpeed)
	fmt.Println("\nИнтересные места рядом:")
	if len(rep.Places) == 0 {
		fmt.Println("ничего не найдено")
	}
	for i, p := range rep.Places {
		fmt.Printf("%2d. %s (%.0f м)\n    %s\n", i+1, p.Name, p.Dist, p.Description)
	}
}