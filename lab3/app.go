package main

import "context"

//возвр коробку итоговым отчетом
func loadReport(l Location, owmKey, geoKey string) *Future[Report] {
	return ThenCombine(
		fetchWeather(context.Background(), l, owmKey),
		ThenCompose(fetchPlaceList(context.Background(), l, geoKey), func(list []geoPlace) *Future[[]Place] {
			fs := make([]*Future[Place], len(list))
			for i, p := range list {
				fs[i] = fetchPlace(context.Background(), p, geoKey, l.Lat, l.Lng)
			}
			return AllOf(fs)
		}),
		func(w Weather, ps []Place) Report { //склеивает погоду с местами и возвр 
			return Report{Location: l, Weather: w, Places: ps}
		},
	)
}