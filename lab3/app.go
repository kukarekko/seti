package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

//загрузка погоды и мест
func loadReport(l Location, owmKey, otmKey string) *Future[Report] {
	return ThenCombine(
		fetchWeather(l, owmKey),
		ThenCompose(fetchPlaceList(l, otmKey), func(list []otmPlace) *Future[[]Place] {
			fs := make([]*Future[Place], len(list))
			for i, p := range list {
				fs[i] = fetchPlace(p, otmKey) // все описания — параллельно
			}
			return AllOf(fs)
		}),
		func(w Weather, ps []Place) Report {
			return Report{Location: l, Weather: w, Places: ps}
		},
	)
}

// вся логика
func app(query string, in *bufio.Reader, ghKey, owmKey, otmKey string) *Future[Report] {
	return ThenCompose(searchLocations(query, ghKey), func(locs []Location) *Future[Report] {
		choose := Async(func() (Location, error) {
			fmt.Println("\nНайденные локации:")
			for i, l := range locs {
				fmt.Printf("  %d. %s\n", i+1, l)
			}
			fmt.Print("Выберите номер: ")
			line, _ := in.ReadString('\n')
			n, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || n < 1 || n > len(locs) {
				return Location{}, fmt.Errorf("неверный номер")
			}
			return locs[n-1], nil
		})
		return ThenCompose(choose, func(l Location) *Future[Report] {
			return loadReport(l, owmKey, otmKey)
		})
	})
}