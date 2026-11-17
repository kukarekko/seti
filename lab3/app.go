package main

import "context"

//возвр коробку итоговым отчетом, прокидываем ctx чтобы можно было отменить
func loadReport(ctx context.Context, l Location, owmKey, geoKey string) *Future[Report] {
	return ThenCombine(
		fetchWeather(ctx, l, owmKey),
		ThenCompose(fetchPlaceList(ctx, l, geoKey), func(list []geoPlace) *Future[[]Place] {
			fs := make([]*Future[Place], len(list))
			for i, p := range list {
				fs[i] = fetchPlace(ctx, p, geoKey, l.Lat, l.Lng)
			}
			return AllOf(fs)
		}),
		func(w Weather, ps []Place) Report { //склеивает погоду с местами и возвр
			return Report{Location: l, Weather: w, Places: ps}
		},
	)
}