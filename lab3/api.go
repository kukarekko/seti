package main

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

var debugOn = os.Getenv("PLACES_DEBUG") != ""

func debugf(format string, args ...any) {
	if debugOn {
		fmt.Printf("DEBUG "+format+"\n", args...)
	}
}

// радиус
const searchRadius = 1000

//статья с координатами дальше этого расстояния (м) считается чужой
const maxWikiDistance = 20000

var bracketsRe = regexp.MustCompile(`\s*\[…\]|\s*\[\d+\]`) //для википедии

//названия вида «Н.А. Борисову», «И.Н. Векуа»
var initialsRe = regexp.MustCompile(`^[А-ЯЁA-Z]\.\s*[А-ЯЁA-Z]\.`)

//чистка текста из википедии
func cleanExtract(s string) string {
	s = bracketsRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\u0301", "")
	s = strings.ReplaceAll(s, "()", "")
	s = strings.ReplaceAll(s, "( ", "(")
	return strings.Join(strings.Fields(s), " ")
}

//растояние между координатами
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

//первые 5 символов для сравнения основы слова
func stem(w string) string {
	r := []rune(w)
	if len(r) > 5 {
		return string(r[:5])
	}
	return w
}

//проверка что статья пересекается с искомым местом
//(минимум 2 слова для длинных названий, 1 для коротких, чтобы не цеплять «Спортсменов»)
func sameTopic(placeName, title string) bool {
	cleanName := initialsRe.ReplaceAllString(placeName, "")
	a := strings.ToLower(cleanName)
	b := strings.ToLower(title)

	words := strings.FieldsFunc(a, func(r rune) bool {
		return !(r >= 'а' && r <= 'я' || r == 'ё' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})

	matches := 0
	for _, w := range words {
		if len([]rune(w)) >= 4 && strings.Contains(b, stem(w)) {
			matches++
		}
	}

	//для коротких названий хватит 1 слова, для длинных нужно 2
	if len(words) <= 2 {
		return matches >= 1
	}
	return matches >= 2
}

//ссылка на картинку из википедии
func commonsURL(img string) string {
	if strings.HasPrefix(img, "http") {
		return img
	}
	img = strings.TrimPrefix(img, "File:")
	img = strings.TrimPrefix(img, "Файл:")
	return "https://commons.wikimedia.org/wiki/Special:FilePath/" + url.PathEscape(img) + "?width=200"
}
//перевод типов мест на русский
func translateKind(resultType, category string) string {
	//сначала смотрим на категорию (она точнее)
	if category != "" {
		dict := map[string]string{
			"public_transport.bus":      "остановка",
			"public_transport.subway":   "метро",
			"public_transport.train":    "станция",
			"catering.cafe":             "кафе",
			"catering.restaurant":       "ресторан",
			"catering.fast_food":        "фастфуд",
			"catering.bar":              "бар",
			"commercial.supermarket":    "супермаркет",
			"commercial.shopping_mall":  "торговый центр",
			"commercial.marketplace":    "рынок",
			"healthcare.pharmacy":       "аптека",
			"healthcare.hospital":       "больница",
			"healthcare.clinic":         "поликлиника",
			"education.school":          "школа",
			"education.university":      "университет",
			"tourism.attraction":        "достопримечательность",
			"tourism.sights":            "достопримечательность",
			"tourism.museum":            "музей",
			"tourism.hotel":             "отель",
			"leisure.park":              "парк",
			"amenity.theatre":           "театр",
			"amenity.cinema":            "кинотеатр",
			"amenity.place_of_worship":  "храм",
			"amenity.bank":              "банк",
			"amenity.atm":               "банкомат",
			"amenity.fuel":              "заправка",
			"amenity.parking":           "парковка",
			"amenity.toilets":           "туалет",
			"office.government":         "госучреждение",
			"building.historic":         "историческое здание",
		}
		//ищем точное совпадение или частичное
		for k, v := range dict {
			if strings.Contains(category, k) {
				return v
			}
		}
	}

	//если категория не подошла, смотрим на result_type
	dict := map[string]string{
		"amenity":         "объект",
		"administrative":  "населенный пункт",
		"populated_place": "населенный пункт",
		"street":          "улица",
		"city":            "город",
		"town":            "город",
		"village":         "деревня",
		"country":         "страна",
		"state":           "регион",
		"attraction":      "достопримечательность",
		"building":        "здание",
		"postcode":        "индекс",
	}
	if rus, ok := dict[resultType]; ok {
		return rus
	}
	return resultType
}

//поиск места
func searchLocations(ctx context.Context, query, key string) *Future[[]Location] {
	q := url.Values{"text": {query}, "limit": {"20"}, "lang": {"ru"}, "apiKey": {key}}
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
	}](ctx, "https://api.geoapify.com/v1/geocode/search?"+q.Encode())

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
			//переводим тип на русский (сначала по категории, потом по result_type)
			rusKind := translateKind(p.ResultType, p.Category)
			res = append(res, Location{
				Name: strings.Join(parts, ", "),
				Kind: rusKind,
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

//поиск погоды
func fetchWeather(ctx context.Context, l Location, key string) *Future[Weather] {
	q := url.Values{"lat": {fmt.Sprint(l.Lat)}, "lon": {fmt.Sprint(l.Lng)}, "units": {"metric"}, "lang": {"ru"}, "appid": {key}}
	f := getJSON[struct {
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp      float64 `json:"temp"`
			FeelsLike float64 `json:"feels_like"`
			Humidity  int     `json:"humidity"`
			Pressure  int     `json:"pressure"`
		} `json:"main"`
		Wind struct {
			Speed float64 `json:"speed"`
		} `json:"wind"`
	}](ctx, "https://api.openweathermap.org/data/2.5/weather?"+q.Encode())

	return Then(f, func(r struct {
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp      float64 `json:"temp"`
			FeelsLike float64 `json:"feels_like"`
			Humidity  int     `json:"humidity"`
			Pressure  int     `json:"pressure"`
		} `json:"main"`
		Wind struct {
			Speed float64 `json:"speed"`
		} `json:"wind"`
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

//поиск мест интересных: только крупные объекты (университеты, музеи, парки, исторические здания)
func fetchPlaceList(ctx context.Context, l Location, key string) *Future[[]geoPlace] {
	q := url.Values{
		"categories": {"education.university,entertainment.museum,leisure.park,building.historic"},
		"filter":     {fmt.Sprintf("circle:%f,%f,%d", l.Lng, l.Lat, searchRadius)},
		"limit":      {"20"},
		"apiKey":     {key},
	}
	f := getJSON[struct {
		Features []struct {
			Properties geoPlace `json:"properties"`
		} `json:"features"`
	}](ctx, "https://api.geoapify.com/v2/places?"+q.Encode())

	res := Then(f, func(r struct {
		Features []struct {
			Properties geoPlace `json:"properties"`
		} `json:"features"`
	}) ([]geoPlace, error) {
		var out []geoPlace
		for _, f := range r.Features {
			//берем только те, у кого есть нормальное название (без инициалов)
			if f.Properties.Name != "" && !initialsRe.MatchString(f.Properties.Name) {
				out = append(out, f.Properties)
			}
		}
		//сортируем по расстоянию
		sort.Slice(out, func(i, j int) bool {
			return out[i].Distance < out[j].Distance
		})
		//оставляем 10
		if len(out) > 10 {
			out = out[:10]
		}
		return out, nil
	})
	return res.orElse(func(error) []geoPlace { return []geoPlace{} })
}

type wikiInfo struct {
	Extract  string
	Thumb    string
	IsPerson bool
}

//определяет, что статья в википедии о человеке а не о месте
func isPersonCategories(titles []string) bool {
	for _, t := range titles {
		if strings.Contains(t, "Родившиеся в") || strings.Contains(t, "Умершие в") ||
			strings.Contains(t, "Персоналии по алфавиту") {
			return true
		}
	}
	return false
}

type wikiInfoResp struct {
	Query struct {
		Pages map[string]struct {
			Extract   string `json:"extract"`
			Thumbnail struct {
				Source string `json:"source"`
			} `json:"thumbnail"`
			Categories []struct {
				Title string `json:"title"`
			} `json:"categories"`
		} `json:"pages"`
	} `json:"query"`
}

//описание и фото из википедии
func fetchWikiInfo(ctx context.Context, title string) *Future[wikiInfo] {
	q := url.Values{
		"action":      {"query"},
		"format":      {"json"},
		"titles":      {title},
		"redirects":   {"1"},
		"prop":        {"extracts|pageimages|categories"},
		"cllimit":     {"max"},
		"exintro":     {"1"},
		"explaintext": {"1"},
		"exsentences": {"3"},
		"piprop":      {"thumbnail"},
		"pithumbsize": {"200"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()

	return Then(getJSON[wikiInfoResp](ctx, u), func(r wikiInfoResp) (wikiInfo, error) {
		for _, p := range r.Query.Pages {
			cats := make([]string, len(p.Categories))
			for i, c := range p.Categories {
				cats[i] = c.Title
			}
			return wikiInfo{
				Extract:  cleanExtract(p.Extract),
				Thumb:    p.Thumbnail.Source,
				IsPerson: isPersonCategories(cats),
			}, nil
		}
		return wikiInfo{}, nil
	}).orElse(func(err error) wikiInfo {
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

//статья по координатам
func fetchRuTitleByCoords(ctx context.Context, lat, lng float64) *Future[string] {
	q := url.Values{
		"action":   {"query"},
		"format":   {"json"},
		"list":     {"geosearch"},
		"gscoord":  {fmt.Sprintf("%f|%f", lat, lng)},
		"gsradius": {"150"},
		"gslimit":  {"1"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()

	return Then(getJSON[wikiGeoResp](ctx, u), func(r wikiGeoResp) (string, error) {
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

//статья по названию: ищем 10 кандидатов и берем первого, который подходит по названию
func fetchRuTitleByName(ctx context.Context, name string) *Future[string] {
	q := url.Values{
		"action":   {"query"},
		"format":   {"json"},
		"list":     {"search"},
		"srsearch": {name},
		"srlimit":  {"10"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()
	return Then(getJSON[wikiSearchResp](ctx, u), func(r wikiSearchResp) (string, error) {
		for _, s := range r.Query.Search {
			if sameTopic(name, s.Title) {
				return s.Title, nil
			}
		}
		return "", nil
	}).orElse(func(error) string { return "" })
}

//уменьшает размер картинки
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

//координаты статьи из википедии
type wikiCoordsResp struct {
	Query struct {
		Pages map[string]struct {
			Coordinates []struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"coordinates"`
		} `json:"pages"`
	} `json:"query"`
}

//получаем координаты статьи чтобы проверить что она про это место
func fetchWikiCoords(ctx context.Context, title string) *Future[[]float64] {
	q := url.Values{
		"action":    {"query"},
		"format":    {"json"},
		"titles":    {title},
		"prop":      {"coordinates"},
		"redirects": {"1"},
	}
	u := "https://ru.wikipedia.org/w/api.php?" + q.Encode()

	return Then(getJSON[wikiCoordsResp](ctx, u), func(r wikiCoordsResp) ([]float64, error) {
		for _, p := range r.Query.Pages {
			if len(p.Coordinates) > 0 {
				return []float64{p.Coordinates[0].Lat, p.Coordinates[0].Lon}, nil
			}
		}
		return nil, nil
	}).orElse(func(error) []float64 { return nil })
}

//собирает все в одно место
func fetchPlace(ctx context.Context, p geoPlace, key string, centerLat, centerLng float64) *Future[Place] {
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

	title := ThenCompose(getJSON[placeDetailsResp](ctx, u), func(r placeDetailsResp) *Future[string] {
		if len(r.Features) > 0 {
			pr := r.Features[0].Properties
			geoDesc = pr.Description
			geoImage = pr.WikiAndMedia.Image
			parts := strings.SplitN(pr.WikiAndMedia.Wikipedia, ":", 2)
			if len(parts) == 2 && parts[0] == "ru" {
				return Async(func() (string, error) { return parts[1], nil })
			}
		}
		return Async(func() (string, error) { return "", nil })
	}).orElse(func(err error) string {
		return ""
	})

	title2 := ThenCompose(title, func(t string) *Future[string] {
		if t != "" {
			return Async(func() (string, error) { return t, nil })
		}
		return ThenCompose(fetchRuTitleByName(ctx, p.Name), func(found string) *Future[string] {
			if found != "" && sameTopic(p.Name, found) {
				return Async(func() (string, error) { return found, nil })
			}
			if p.Lat == 0 || p.Lon == 0 {
				return Async(func() (string, error) { return "", nil })
			}
			return Then(fetchRuTitleByCoords(ctx, p.Lat, p.Lon), func(c string) (string, error) {
				if sameTopic(p.Name, c) {
					return c, nil
				}
				return "", nil
			})
		})
	})

	return ThenCompose(title2, func(t string) *Future[Place] {
		if t == "" {
			return ready(wikiInfo{}, nil)
		}

		// Проверяем координаты статьи
		return ThenCompose(fetchWikiCoords(ctx, t), func(coords []float64) *Future[Place] {
			if len(coords) == 2 {
				distToArticle := distanceMeters(p.Lat, p.Lon, coords[0], coords[1])
				// Если статья дальше 20 км — это не то место!
				if distToArticle > maxWikiDistance {
					return ready(wikiInfo{}, nil)
				}
			}

			info := fetchWikiInfo(ctx, t)
			return ThenCompose(info, func(wi wikiInfo) *Future[Place] {
				//если статья про человека — не берем ни описание, ни портрет
				if wi.IsPerson {
					wi = wikiInfo{}
				}
				thumb := wi.Thumb
				if thumb == "" && geoImage != "" {
					thumb = commonsURL(geoImage)
				}
				if thumb == "" {
					return ready(wi, nil)
				}
				return Then(getImage(ctx, thumb, 120, 90), func(img *image.RGBA) (Place, error) {
					return finish(wi, img), nil
				}).orElse(func(err error) Place {
					return finish(wi, nil)
				})
			})
		})
	}).orElse(func(err error) Place {
		return finish(wikiInfo{}, nil)
	})
}

//шаблонное описание если ничего не найдено
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

//обрезка описания
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

//перенос по сторокам
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