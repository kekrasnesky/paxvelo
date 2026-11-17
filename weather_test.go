package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNWS is the weather service: one grid point whose hourly forecast is
// 61°F, mostly sunny, 20% rain, every hour for a week.
func fakeNWS(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("the weather service asks for a User-Agent")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/points/"):
			fmt.Fprintf(w, `{"properties":{"forecastHourly":"%s/gridpoints/LWX/120,30/forecast/hourly"}}`, srv.URL)
		case strings.HasSuffix(r.URL.Path, "/forecast/hourly"):
			start := time.Now().Truncate(time.Hour).Add(-time.Hour)
			var ps []map[string]any
			for i := range 24 * 7 {
				ps = append(ps, map[string]any{"startTime": start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), "temperature": 61, "temperatureUnit": "F",
					"shortForecast": "Mostly Sunny", "windSpeed": "5 to 10 mph", "windDirection": "SW", "probabilityOfPrecipitation": map[string]any{"value": 20}})
			}
			json.NewEncoder(w).Encode(map[string]any{"properties": map[string]any{"periods": ps}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// A ride in the next few days shows the forecast for its start; virtual,
// cancelled and far-off rides don't; and the forecast is read at most every
// half hour.
func TestRideForecast(t *testing.T) {
	a, h, _ := rideApp(t)
	srv, calls := fakeNWS(t)
	a.weather = newWeather(a.cfg.SiteURL)
	a.weather.Base = srv.URL
	day := func(n int) string { return a.today().AddDate(0, 0, n).Format("2006-01-02") }
	soon, _ := a.store.AddRide(CalRide{Date: day(2), Time: "08:00", Name: "Coffee Spin"})
	zwift, _ := a.store.AddRide(CalRide{Date: day(2), Time: "18:00", Name: "Zwift Spin", Virtual: "yes"})
	off, _ := a.store.AddRide(CalRide{Date: day(3), Time: "08:00", Name: "Rainy", Cancelled: true})
	far, _ := a.store.AddRide(CalRide{Date: day(12), Time: "08:00", Name: "Later"})

	a.readWeather(context.Background())
	n := calls.Load()
	page := func(key string) string { return do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String() }
	if p := page(soon.Key); !strings.Contains(p, `class="ride-weather"`) || !strings.Contains(p, "<strong>8 AM</strong> 61°F · Mostly Sunny · 20% rain · wind SW 5 to 10 mph") {
		t.Error("the ride page should show the forecast for 8 AM")
	}
	for _, k := range []string{zwift.Key, off.Key, far.Key} {
		if strings.Contains(page(k), `class="ride-weather"`) {
			t.Errorf("%s shouldn't have a forecast", k)
		}
	}
	a.readWeather(context.Background())
	if calls.Load() != n {
		t.Errorf("the forecast was read again within half an hour: %d calls, then %d", n, calls.Load())
	}
	// Without the weather service, pages still work.
	a.weather = nil
	if !strings.Contains(page(soon.Key), "Coffee Spin") || strings.Contains(page(soon.Key), `class="ride-weather"`) {
		t.Error("no weather service, no forecast")
	}
}
