package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The forecast for a ride's start, from the US National Weather Service
// (api.weather.gov: free, no account). Every 30 minutes the site reads the
// hourly forecast for the starts of the rides in the next six days: the
// start's map pin from its Strava event, or the club's home (Leonardtown)
// when it has none. Ride pages show it from what was read, so a slow or
// down weather service never slows a page. Virtual and cancelled rides get
// none.

const (
	weatherAhead = 6 * 24 * time.Hour // the hourly forecast reaches about a week
	weatherEvery = 30 * time.Minute
	homeLat      = 38.2912 // Leonardtown, MD: where most club rides start
	homeLng      = -76.6355
)

// Forecast is the weather for one hour.
type Forecast struct {
	At      time.Time
	Temp    int
	Unit    string // F
	Short   string // "Partly Sunny"
	Rain    int    // chance of rain, percent
	Wind    string // "5 to 10 mph"
	WindDir string // "SW"
}

// Line is the forecast in a few words: "62°F · Partly Sunny · 10% rain ·
// wind SW 5 to 10 mph".
func (f Forecast) Line() string {
	parts := []string{fmt.Sprintf("%d°%s", f.Temp, f.Unit)}
	if f.Short != "" {
		parts = append(parts, f.Short)
	}
	if f.Rain > 0 {
		parts = append(parts, fmt.Sprintf("%d%% rain", f.Rain))
	}
	if f.Wind != "" {
		w := "wind " + f.Wind
		if f.WindDir != "" {
			w = "wind " + f.WindDir + " " + f.Wind
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, " · ")
}

// Hour is when it's for: "8 AM".
func (f Forecast) Hour() string { return f.At.In(clubTZ).Format("3 PM") }

type weatherService struct {
	Base   string       // https://api.weather.gov; a test server in tests
	Client *http.Client // nil: one with a timeout
	agent  string

	mu     sync.Mutex
	points map[string]string       // "38.29,-76.64" → its hourly forecast address
	hourly map[string]hourlyResult // hourly forecast address → what it said
}

type hourlyResult struct {
	Periods []Forecast
	Read    time.Time
}

func newWeather(siteURL string) *weatherService {
	return &weatherService{Base: "https://api.weather.gov", agent: "PaxVelo club calendar (" + siteURL + ")",
		points: map[string]string{}, hourly: map[string]hourlyResult{}}
}

// spotKey rounds a place to about a kilometre, which is finer than the
// forecast's own grid.
func spotKey(lat, lng float64) string { return fmt.Sprintf("%.2f,%.2f", lat, lng) }

// rideSpot is where a ride's forecast is for, and false for one that gets
// none (virtual).
func rideSpot(o RideOcc) (lat, lng float64, ok bool) {
	if o.IsVirtual() {
		return 0, 0, false
	}
	if o.Strava != nil && o.Strava.Lat != 0 {
		return o.Strava.Lat, o.Strava.Lng, true
	}
	return homeLat, homeLng, true
}

// rideStart is when a ride starts; a ride with no time, 9 am.
func (a *App) rideStart(o RideOcc) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02 15:04", o.Date+" "+or(o.Time, "09:00"), a.cfg.Location)
	return t, err == nil
}

// forecastFor is the forecast at a ride's start, from what's been read.
func (a *App) forecastFor(o RideOcc) (Forecast, bool) {
	if a.weather == nil || o.Cancelled {
		return Forecast{}, false
	}
	lat, lng, ok := rideSpot(o)
	at, okT := a.rideStart(o)
	if !ok || !okT || at.Before(time.Now().Add(-time.Hour)) || at.After(time.Now().Add(weatherAhead)) {
		return Forecast{}, false
	}
	return a.weather.at(spotKey(lat, lng), at)
}

func (w *weatherService) at(spot string, at time.Time) (Forecast, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h, ok := w.hourly[w.points[spot]]
	if !ok || time.Since(h.Read) > 6*time.Hour {
		return Forecast{}, false
	}
	for _, p := range h.Periods {
		if !at.Before(p.At) && at.Before(p.At.Add(time.Hour)) {
			return p, true
		}
	}
	return Forecast{}, false
}

func (w *weatherService) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", w.agent)
	req.Header.Set("Accept", "application/geo+json")
	c := w.Client
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// read updates the hourly forecast for one place, if it's over half an
// hour old.
func (w *weatherService) read(ctx context.Context, lat, lng float64) error {
	spot := spotKey(lat, lng)
	w.mu.Lock()
	hourlyURL := w.points[spot]
	fresh := hourlyURL != "" && time.Since(w.hourly[hourlyURL].Read) < weatherEvery-time.Minute
	w.mu.Unlock()
	if fresh {
		return nil
	}
	if hourlyURL == "" {
		var pt struct {
			Properties struct {
				ForecastHourly string `json:"forecastHourly"`
			} `json:"properties"`
		}
		if err := w.get(ctx, fmt.Sprintf("%s/points/%.4f,%.4f", strings.TrimRight(w.Base, "/"), lat, lng), &pt); err != nil {
			return err
		}
		if hourlyURL = pt.Properties.ForecastHourly; hourlyURL == "" {
			return fmt.Errorf("no hourly forecast for %s", spot)
		}
		w.mu.Lock()
		w.points[spot] = hourlyURL
		w.mu.Unlock()
	}
	var fc struct {
		Properties struct {
			Periods []struct {
				StartTime       time.Time `json:"startTime"`
				Temperature     int       `json:"temperature"`
				TemperatureUnit string    `json:"temperatureUnit"`
				ShortForecast   string    `json:"shortForecast"`
				WindSpeed       string    `json:"windSpeed"`
				WindDirection   string    `json:"windDirection"`
				Precip          struct {
					Value *int `json:"value"`
				} `json:"probabilityOfPrecipitation"`
			} `json:"periods"`
		} `json:"properties"`
	}
	if err := w.get(ctx, hourlyURL, &fc); err != nil {
		return err
	}
	res := hourlyResult{Read: time.Now()}
	for _, p := range fc.Properties.Periods {
		f := Forecast{At: p.StartTime, Temp: p.Temperature, Unit: or(p.TemperatureUnit, "F"), Short: p.ShortForecast, Wind: p.WindSpeed, WindDir: p.WindDirection}
		if p.Precip.Value != nil {
			f.Rain = *p.Precip.Value
		}
		res.Periods = append(res.Periods, f)
	}
	w.mu.Lock()
	w.hourly[hourlyURL] = res
	w.mu.Unlock()
	return nil
}

// readWeather reads the forecast for every place a ride starts in the next
// six days.
func (a *App) readWeather(ctx context.Context) {
	today := a.today()
	seen := map[string]bool{}
	for _, o := range a.store.Rides(today, today.Add(weatherAhead)) {
		lat, lng, ok := rideSpot(o)
		if !ok || o.Cancelled || seen[spotKey(lat, lng)] {
			continue
		}
		seen[spotKey(lat, lng)] = true
		if err := a.weather.read(ctx, lat, lng); err != nil {
			log.Printf("weather: %v", err)
		}
	}
}

// localSite says the site is a copy running on someone's computer
// (SITE_URL on localhost), which doesn't read the forecast.
func localSite(siteURL string) bool {
	u, err := url.Parse(siteURL)
	if err != nil {
		return true
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

func (a *App) weatherLoop(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		a.readWeather(c)
		cancel()
		select {
		case <-time.After(weatherEvery):
		case <-ctx.Done():
			return
		}
	}
}
