package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

//делает GET-запрос по адресу u и разбирает ответ в структуру T
func getJSON[T any](u string) *Future[T] {
	return Async(func() (T, error) {
		var out T
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil) //ашттп клиент глобальный
		if err != nil {
			return out, err
		}
		req.Header.Set("Accept", "application/json") //добавляем заголовоу
		resp, err := httpClient.Do(req)
		if err != nil {
			return out, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return out, fmt.Errorf("HTTP %d: %s://%s%s", resp.StatusCode, req.URL.Scheme, req.URL.Host, req.URL.Path)
		}
		return out, json.NewDecoder(resp.Body).Decode(&out)
	})
}

// GraphHopper: геокодинг

func searchLocations(query, key string) *Future[[]Location] {
	q := url.Values{"q": {query}, "locale": {"ru"}, "limit": {"5"}, "key": {key}}
	f := getJSON[struct {
		Hits []struct {
			Name, Street, Housenumber, City, State, Country string
			Point                                           struct{ Lat, Lng float64 }
		} `json:"hits"`
	}]("https://graphhopper.com/api/1/geocode?" + q.Encode())

	return Then(f, func(r struct {
		Hits []struct {
			Name, Street, Housenumber, City, State, Country string
			Point                                           struct{ Lat, Lng float64 }
		} `json:"hits"`
	}) ([]Location, error) {
		var res []Location
		for _, h := range r.Hits {
			parts := []string{}
			for _, p := range []string{h.Name, h.Street, h.Housenumber, h.City, h.State, h.Country} {
				if p != "" && (len(parts) == 0 || parts[len(parts)-1] != p) {
					parts = append(parts, p)
				}
			}
			res = append(res, Location{strings.Join(parts, ", "), h.Point.Lat, h.Point.Lng})
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("ничего не найдено")
		}
		return res, nil
	})
}

// OpenWeatherMap

func fetchWeather(l Location, key string) *Future[Weather] {
	q := url.Values{
		"lat": {fmt.Sprint(l.Lat)}, "lon": {fmt.Sprint(l.Lng)},
		"units": {"metric"}, "lang": {"ru"}, "appid": {key},
	}
	f := getJSON[struct {
		Weather []struct{ Description string } `json:"weather"`
		Main    struct {
			Temp, FeelsLike float64
			Humidity, Pressure int
		} `json:"main"`
		Wind struct{ Speed float64 }
	}]("https://api.openweathermap.org/data/2.5/weather?" + q.Encode())

	return Then(f, func(r struct {
		Weather []struct{ Description string } `json:"weather"`
		Main    struct {
			Temp, FeelsLike float64
			Humidity, Pressure int
		} `json:"main"`
		Wind struct{ Speed float64 }
	}) (Weather, error) {
		w := Weather{
			Temp: r.Main.Temp, Feels: r.Main.FeelsLike,
			Humidity: r.Main.Humidity, Pressure: r.Main.Pressure, WindSpeed: r.Wind.Speed,
		}
		if len(r.Weather) > 0 {
			w.Description = r.Weather[0].Description
		}
		return w, nil
	})
}

// OpenTripMap: список мест 

type otmPlace struct {
	Xid, Name, Kinds string
	Dist             float64
}

func fetchPlaceList(l Location, key string) *Future[[]otmPlace] {
	q := url.Values{
		"radius": {"1000"}, "lat": {fmt.Sprint(l.Lat)}, "lon": {fmt.Sprint(l.Lng)},
		"limit": {"20"}, "rate": {"2"}, "format": {"json"}, "apikey": {key},
	}
	f := getJSON[[]otmPlace]("https://api.opentripmap.com/0.1/ru/places/radius?" + q.Encode())
	return Then(f, func(ps []otmPlace) ([]otmPlace, error) {
		var out []otmPlace
		for _, p := range ps {
			if p.Name != "" {
				out = append(out, p)
			}
			if len(out) == 10 {
				break
			}
		}
		return out, nil
	})
}

//OpenTripMap: описание места 

func fetchPlace(p otmPlace, key string) *Future[Place] {
	place := Place{Name: p.Name, Kinds: p.Kinds, Dist: p.Dist}
	u := "https://api.opentripmap.com/0.1/ru/places/xid/" +
		url.PathEscape(p.Xid) + "?apikey=" + url.QueryEscape(key)

	f := getJSON[struct {
		Wiki struct{ Text string } `json:"wikipedia_extracts"`
		Info struct{ Descr string } `json:"info"`
	}](u)

	res := Then(f, func(d struct {
		Wiki struct{ Text string } `json:"wikipedia_extracts"`
		Info struct{ Descr string } `json:"info"`
	}) (Place, error) {
		text := d.Wiki.Text
		if text == "" {
			text = d.Info.Descr
		}
		if text == "" {
			text = "(описание отсутствует)"
		}
		r := []rune(strings.Join(strings.Fields(text), " "))
		if len(r) > 300 {
			text = string(r[:300]) + "…"
		}
		place.Description = text
		return place, nil
	})
	// одно упавшее описание не роняет весь список
	return res.orElse(func(error) Place {
		place.Description = "(не удалось получить описание)"
		return place
	})
}