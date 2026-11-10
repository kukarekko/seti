package main

func loadReport(l Location, owmKey, geoKey string) *Future[Report] {
	return ThenCombine(
		fetchWeather(l, owmKey),
		ThenCompose(fetchPlaceList(l, geoKey), func(list []geoPlace) *Future[[]Place] {
			fs := make([]*Future[Place], len(list))
			for i, p := range list {
				fs[i] = fetchPlace(p, geoKey, l.Lat, l.Lng)
			}
			return AllOf(fs)
		}),
		func(w Weather, ps []Place) Report {
			return Report{Location: l, Weather: w, Places: ps}
		},
	)
}