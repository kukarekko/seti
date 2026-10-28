package main

import "fmt"

type Location struct {
	Name string
	Lat, Lng float64 //широта, долгота
}

func (l Location) String() string {
	return fmt.Sprintf("%s (%.5f, %.5f)", l.Name, l.Lat, l.Lng)
}

type Weather struct {
	Description string //описание
	Temp, Feels float64
	Humidity int //влажность
	Pressure int //давление
	WindSpeed float64
}

type Place struct {
	Name, Kinds, Description string
	Dist float64 //раст до центра
}

type Report struct {
	Location Location
	Weather Weather
	Places []Place
}