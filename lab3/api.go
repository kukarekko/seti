package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

const userAgent = "PlacesApp/1.0 (учебный проект)"

var bracketsRe = regexp.MustCompile(`\s*\[…\]|\s*\[\d+\]`)

func cleanExtract(s string) string {
	s = bracketsRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\u0301", "")
	s = strings.ReplaceAll(s, "()", "")
	s = strings.ReplaceAll(s, "( ", "(")
	return strings.Join(strings.Fields(s), " ")
}

func asyncGet(u string) <-chan []byte {
	out := make(chan []byte, 1)
	go func() {
		defer close(out)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			fmt.Printf("DEBUG asyncGet NewRequest: %v\n", err)
			out <- nil
			return
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := httpClient.Do(req)
		if err != nil {
			fmt.Printf("DEBUG asyncGet Do: %v\n", err)
			out <- nil
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("DEBUG asyncGet status: %d %s\n", resp.StatusCode, u)
			out <- nil
			return
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("DEBUG asyncGet ReadAll: %v\n", err)
			out <- nil
			return
		}
		out <- body
	}()
	return out
}

func getJSON[T any](u string) *Future[T] {
	return Async(func() (T, error) {
		var out T
		body := <-asyncGet(u)
		if body == nil {
			return out, fmt.Errorf("HTTP request failed")
		}
		return out, json.Unmarshal(body, &out)
	})
}

func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	deltaPhi := (lat2 - lat1) * math.Pi / 180
	deltaLambda := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaPhi/2)*math.Sin(deltaPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*
			math.Sin(deltaLambda/2)*math.Sin(deltaLambda/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func stem(w string) string {
	r := []rune(w)
	if len(r) > 5 {
		return string(r[:5])
	}
	return w
}

func sameTopic(placeName, title string) bool {
	a := strings.ToLower(placeName)
	b := strings.ToLower(title)
	clean := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool {
			return !(r >= 'а' && r <= 'я' || r == 'ё' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
	}
	for _, w := range clean(a) {
		if len([]rune(w)) >= 4 && strings.Contains(b, stem(w)) {
			return true
		}
	}
	for _, w := range clean(b) {
		if len([]rune(w)) >= 4 && strings.Contains(a, stem(w)) {
			return true
		}
	}
	return false
}

func commonsURL(img string) string {
	if strings.HasPrefix(img, "http") {
		return img
	}
	img = strings.TrimPrefix(img, "File:")
	img = strings.TrimPrefix(img, "Файл:")
	return "https://commons.wikimedia.org/wiki/Special:FilePath/" + url.PathEscape(img) + "?width=200"
}

func searchLocations(query, key string) *Future[[]Location] {
	q := url.Values{"text": {query}, "limit": {"5"}, "lang": {"ru"}, "apiKey": {key}}
	f := getJSON[struct {
		Features []struct {
			Properties struct {
				Name       string  `json:"name"`
				Address    string  `json:"address_line1"`
				City       string  `json:"city"`
				ResultType string  `json:"result_type"`
				Category   string  `json:"category"`
				Lat        float64 `json:"lat"`
				Lon        float64 `json:"lon"`
			} `json:"properties"`
		} `json:"features"`
	}]("https://api.geoapify.com/v1/geocode/search?" + q.Encode())

	return Then(f, func(r struct {
		Features []struct {
			Properties struct {
				Name       string  `json:"name"`
				Address    string  `json:"address_line1"`
				City       string  `json:"city"`
				ResultType string  `json:"result_type"`
				Category   string  `json:"category"`
				Lat        float64 `json:"lat"`
				Lon        float64 `json:"lon"`
			} `json:"properties"`
		} `json:"features"`
	}) ([]Location, error) {
		var res []Location
		for _, f := range r.Features {
			p := f.Properties
			name := p.Name
			if name == "" {
				name = p.Address
			}
			parts := []string{name}
			if p.City != "" && p.City != name {
				parts = append(parts, p.City)
			}
			kind := p.Category
			if kind == "" {
				kind = p.ResultType
			}
			res = append(res, Location{
				Name: strings.Join(parts, ", "),
				Kind: kind,
				Lat:  p.Lat,
				Lng:  p.Lon,
			})
		}
		if len(res) == 0 {
			return nil, fmt.Errorf("ничего не найдено")
		}
		return res, nil
	})
}

func fetchWeather(l Location, key string) *Future[Weather] {
	q := url.Values{"lat": {fmt.Sprint(l.Lat)}, "lon": {fmt.Sprint(l.Lng)}, "units": {"metric"}, "lang": {"ru"}, "appid": {key}}
	f := getJSON[struct {
		Weather []struct{ Description string } `json:"weather"`
		Main    struct {
			Temp, FeelsLike    float64
			Humidity, Pressure int
		} `json:"main"`
		Wind struct{ Speed float64 }
	}]("https://api.openweathermap.org/data/2.5/weather?" + q.Encode())

	return Then(f, func(r struct {
		Weather []struct{ Description string } `json:"weather"`
		Main    struct {
			Temp, FeelsLike    float64
			Humidity, Pressure int
		} `json:"main"`
		Wind struct{ Speed float64 }
	}) (Weather, error) {
		w := Weather{Temp: r.Main.Temp, Feels: r.Main.FeelsLike, Humidity: r.Main.Humidity, Pressure: r.Main.Pressure, WindSpeed: r.Wind.Speed}
		if len(r.Weather) > 0 {
			w.Description = r.Weather[0].Description
		}
		return w, nil
	})
}

type geoPlace struct {
	PlaceId    string   `json:"place_id"`
	Name       string   `json:"name"`
	Distance   float64  `json:"distance"`
	Categories []string `json:"categories"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
}

func fetchPlaceList(l Location, key string) *Future[[]geoPlace] {
	q := url.Values{"categories": {"tourism.sights"}, "filter": {fmt.Sprintf("circle:%f,%f,1000", l.Lng, l.Lat)}, "limit": {"10"}, "apiKey": {key}}
	f := getJSON[struct {
		Features []struct{ Properties geoPlace `json:"properties"` } `json:"features"`
	}]("https://api.geoapify.com/v2/places?" + q.Encode())

	res := Then(f, func(r struct {
		Features []struct{ Properties geoPlace `json:"properties"` } `json:"features"`
	}) ([]geoPlace, error) {
		var out []geoPlace
		for _, f := range r.Features {
			if f.Properties.Name != "" {
				out = append(out, f.Properties)
			}
		}
		return out, nil
	})
	return res.orElse(func(error) []geoPlace { return []geoPlace{} })
}

type wikiInfo struct {
	Extract string
	Thumb   string
}

type wikiInfoResp struct {
	Query struct {
		Pages map[string]struct {
			Extract   string `json:"extract"`
			Thumbnail struct {
				Source string `json:"source"`
			} `json:"thumbnail"`
		} `json:"pages"`
	} `json:"query"`
}

func fetchWikiInfo(title string) *Future[wikiInfo] {
	q := url.Values{
		"action":      {"query"},
		"format":      {"json"},
		"titles":      {title},
		"redirects":   {"1"},
		"prop":        {"extracts|pageimages"},
		"exintro":     {"1"},
		"explaintext": {"1"},
		"exsentences": {"3"},
		"piprop":      {"thumbnail"},
		"pithumbsize": {"200"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()
	fmt.Printf("DEBUG запрос Википедии: %s\n", u)

	return Then(getJSON[wikiInfoResp](u), func(r wikiInfoResp) (wikiInfo, error) {
		for _, p := range r.Query.Pages {
			fmt.Printf("DEBUG Вики ответ: extract=%d симв., thumb=%q\n", len(p.Extract), p.Thumbnail.Source)
			return wikiInfo{Extract: cleanExtract(p.Extract), Thumb: p.Thumbnail.Source}, nil
		}
		fmt.Printf("DEBUG Вики: pages пустой\n")
		return wikiInfo{}, nil
	}).orElse(func(err error) wikiInfo {
		fmt.Printf("DEBUG Ошибка Википедии: %v\n", err)
		return wikiInfo{}
	})
}

type wikiGeoResp struct {
	Query struct {
		Geosearch []struct {
			Title string `json:"title"`
		} `json:"geosearch"`
	} `json:"query"`
}

func fetchRuTitleByCoords(lat, lng float64) *Future[string] {
	q := url.Values{
		"action":   {"query"},
		"format":   {"json"},
		"list":     {"geosearch"},
		"gscoord":  {fmt.Sprintf("%f|%f", lat, lng)},
		"gsradius": {"150"},
		"gslimit":  {"1"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()

	return Then(getJSON[wikiGeoResp](u), func(r wikiGeoResp) (string, error) {
		if len(r.Query.Geosearch) > 0 {
			return r.Query.Geosearch[0].Title, nil
		}
		return "", nil
	}).orElse(func(error) string { return "" })
}

type wikiSearchResp struct {
	Query struct {
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
}

func fetchRuTitleByName(name string) *Future[string] {
	q := url.Values{
		"action":   {"query"},
		"format":   {"json"},
		"list":     {"search"},
		"srsearch": {name},
		"srlimit":  {"1"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()
	return Then(getJSON[wikiSearchResp](u), func(r wikiSearchResp) (string, error) {
		if len(r.Query.Search) > 0 {
			return r.Query.Search[0].Title, nil
		}
		return "", nil
	}).orElse(func(error) string { return "" })
}

func resizeFit(src image.Image, maxW, maxH int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	if scale > 1 {
		scale = 1
	}
	nw := max(1, int(float64(w)*scale))
	nh := max(1, int(float64(h)*scale))

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/nw, b.Min.Y+y*h/nh))
		}
	}
	return dst
}

func fetchImage(u string) *Future[*image.RGBA] {
	return Async(func() (*image.RGBA, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("картинка недоступна: %d", resp.StatusCode)
		}
		img, _, err := image.Decode(io.LimitReader(resp.Body, 5<<20))
		if err != nil {
			return nil, err
		}
		return resizeFit(img, 120, 90), nil
	})
}

type placeDetailsResp struct {
	Features []struct {
		Properties struct {
			Description  string `json:"description"`
			WikiAndMedia struct {
				Wikipedia string `json:"wikipedia"`
				Image     string `json:"image"`
			} `json:"wiki_and_media"`
		} `json:"properties"`
	} `json:"features"`
}

func fetchPlace(p geoPlace, key string, centerLat, centerLng float64) *Future[Place] {
	dist := p.Distance
	if dist == 0 && p.Lat != 0 && p.Lon != 0 {
		dist = distanceMeters(centerLat, centerLng, p.Lat, p.Lon)
	}
	place := Place{Name: p.Name, Dist: dist, Lat: p.Lat, Lng: p.Lon}
	geoDesc := ""
	geoImage := ""

	finish := func(wi wikiInfo, img *image.RGBA) Place {
		switch {
		case wi.Extract != "":
			place.Description = wi.Extract
		case geoDesc != "":
			place.Description = geoDesc
		default:
			place.Description = generateDescription(p.Name, p.Categories)
		}
		place.Image = img
		return place
	}
	ready := func(wi wikiInfo, img *image.RGBA) *Future[Place] {
		return Async(func() (Place, error) { return finish(wi, img), nil })
	}

	u := "https://api.geoapify.com/v2/place-details?id=" + url.QueryEscape(p.PlaceId) +
		"&lang=ru&apiKey=" + url.QueryEscape(key)

	title := ThenCompose(getJSON[placeDetailsResp](u), func(r placeDetailsResp) *Future[string] {
		if len(r.Features) > 0 {
			pr := r.Features[0].Properties
			geoDesc = pr.Description
			geoImage = pr.WikiAndMedia.Image
			parts := strings.SplitN(pr.WikiAndMedia.Wikipedia, ":", 2)
			if len(parts) == 2 {
				return Async(func() (string, error) { return parts[1], nil })
			}
		}
		return Async(func() (string, error) { return "", nil })
	}).orElse(func(err error) string {
		fmt.Printf("DEBUG [%s] Ошибка Geoapify: %v\n", p.Name, err)
		return ""
	})

	title2 := ThenCompose(title, func(t string) *Future[string] {
		if t != "" {
			return Async(func() (string, error) { return t, nil })
		}
		return ThenCompose(fetchRuTitleByName(p.Name), func(found string) *Future[string] {
			fmt.Printf("DEBUG [%s] Поиск по имени: %q (совпадает: %v)\n", p.Name, found, sameTopic(p.Name, found))
			if found != "" && sameTopic(p.Name, found) {
				return Async(func() (string, error) { return found, nil })
			}
			if p.Lat == 0 || p.Lon == 0 {
				return Async(func() (string, error) { return "", nil })
			}
			return Then(fetchRuTitleByCoords(p.Lat, p.Lon), func(c string) (string, error) {
				fmt.Printf("DEBUG [%s] Поиск по координатам: %q (совпадает: %v)\n", p.Name, c, sameTopic(p.Name, c))
				if sameTopic(p.Name, c) {
					return c, nil
				}
				return "", nil
			})
		})
	})

	return ThenCompose(title2, func(t string) *Future[Place] {
		info := Async(func() (wikiInfo, error) { return wikiInfo{}, nil })
		if t != "" {
			info = fetchWikiInfo(t)
		}
		return ThenCompose(info, func(wi wikiInfo) *Future[Place] {
			fmt.Printf("DEBUG [%s] Вики: thumb=%q, extract_len=%d\n", p.Name, wi.Thumb, len(wi.Extract))

			thumb := wi.Thumb
			if thumb == "" && geoImage != "" {
				thumb = commonsURL(geoImage)
				fmt.Printf("DEBUG [%s] Использую картинку Geoapify: %q\n", p.Name, thumb)
			}
			if thumb == "" {
				fmt.Printf("DEBUG [%s] Фото нет\n", p.Name)
				return ready(wi, nil)
			}
			return Then(fetchImage(thumb), func(img *image.RGBA) (Place, error) {
				fmt.Printf("DEBUG [%s] Фото загружено: %dx%d\n", p.Name, img.Bounds().Dx(), img.Bounds().Dy())
				return finish(wi, img), nil
			}).orElse(func(err error) Place {
				fmt.Printf("DEBUG [%s] Ошибка загрузки фото: %v\n", p.Name, err)
				return finish(wi, nil)
			})
		})
	}).orElse(func(err error) Place {
		fmt.Printf("DEBUG [%s] Общая ошибка: %v\n", p.Name, err)
		return finish(wikiInfo{}, nil)
	})
}

func generateDescription(name string, categories []string) string {
	objType := ""
	for _, c := range categories {
		parts := strings.Split(c, ".")
		last := parts[len(parts)-1]
		switch last {
		case "memorial":
			objType = "мемориал"
		case "monument":
			objType = "памятник"
		case "museum":
			objType = "музей"
		case "attraction":
			objType = "достопримечательность"
		case "park":
			objType = "парк"
		}
		if objType != "" {
			break
		}
	}
	if objType == "" {
		objType = "объект"
	}

	lowerName := strings.ToLower(name)

	switch objType {
	case "мемориал":
		if strings.Contains(lowerName, "аллея") {
			return "Мемориальная аллея «" + name + "»."
		}
		return "Памятное место «" + name + "»."
	case "памятник":
		return "Памятник, установленный в честь «" + name + "»."
	case "музей":
		return "Музей «" + name + "», где представлены экспонаты, связанные с этой темой."
	case "парк":
		return "Парк «" + name + "» — зелёная зона для отдыха и прогулок."
	case "достопримечательность":
		if strings.Contains(lowerName, "площадь") {
			return "Городская площадь «" + name + "» — популярное место для прогулок."
		}
		return "Достопримечательность «" + name + "»."
	default:
		return "Объект «" + name + "»."
	}
}

func shorten(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	cut := r[:maxRunes]
	for i := len(cut) - 1; i > maxRunes/2; i-- {
		if cut[i] == '.' || cut[i] == '!' || cut[i] == '?' {
			return string(cut[:i+1])
		}
	}
	for i := len(cut) - 1; i > 0; i-- {
		if cut[i] == ' ' {
			return string(cut[:i]) + "…"
		}
	}
	return string(cut) + "…"
}

func wrapText(s string, width int, indent string) string {
	var sb strings.Builder
	lineLen := 0
	sb.WriteString(indent)
	for _, w := range strings.Fields(s) {
		wl := len([]rune(w))
		if lineLen > 0 && lineLen+1+wl > width {
			sb.WriteString("\n" + indent)
			lineLen = 0
		}
		if lineLen > 0 {
			sb.WriteString(" ")
			lineLen++
		}
		sb.WriteString(w)
		lineLen += wl
	}
	return sb.String()
}