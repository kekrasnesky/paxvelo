package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config comes from paxvelo.conf (KEY=value lines). Environment variables with
// the same names override the file. See paxvelo.conf.example for every option.
type Config struct {
	SiteURL       string
	Listen        string
	DataDir       string
	AdminPassword string
	AdminEmail    string
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPass      string
	FromName      string
	FromEmail     string
	SendDelay     time.Duration
	EmailLimit    int // emails the provider allows a day
	Location      *time.Location

	// Club links and dues
	DuesIndividual   int
	DuesFamily       int
	PayPalIndividual string
	PayPalFamily     string
	KitURL           string
	StravaURL        string
	DonateURL        string

	// Ride calendar: the Strava import, admins' "Sign in with Strava", and
	// notices about ride leaders
	StravaClientID     string
	StravaClientSecret string
	StravaClubID       string // from STRAVA_URL unless set
	StravaAPI          string // https://www.strava.com; tests point it elsewhere
	RidesEmail         string // where ride leader notices go
	CalendarAPIKey     string // lets scripts change the calendar through /api

	// Where the club's Instagram feed links point (Behold's feed server);
	// tests point it elsewhere
	InstagramFeeds string
}

const defaultKitURL = "https://jakroo.com/store-front?storeId=rJgx0naZoge&titleProp=paxvelo&bannerProp=https://jakroo.storage.googleapis.com/team_stores_banner/ryYi5fMige-8e8f3f54b2b9f-blob"

// Dues returns the yearly dues for a plan ("individual" or "family").
func (c Config) Dues(plan string) int {
	if plan == "family" {
		return c.DuesFamily
	}
	return c.DuesIndividual
}

// PayLink returns the PayPal payment link for a plan, or "" if not set up yet.
func (c Config) PayLink(plan string) string {
	if plan == "family" {
		return c.PayPalFamily
	}
	return c.PayPalIndividual
}

// ThisYear is the current dues year.
func (c Config) ThisYear() int { return time.Now().In(c.Location).Year() }

func cleanPlan(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "family") {
		return "family"
	}
	return "individual"
}

func planLabel(plan string) string {
	if plan == "family" {
		return "Family"
	}
	return "Individual"
}

func loadConfig() (Config, error) {
	path := "paxvelo.conf"
	if p := os.Getenv("PAXVELO_CONFIG"); p != "" {
		path = p
	}
	file := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		file[strings.TrimSpace(k)] = v
	}
	get := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v := file[key]; v != "" {
			return v
		}
		return def
	}

	c := Config{
		SiteURL:       strings.TrimRight(get("SITE_URL", "http://localhost:8080"), "/"),
		Listen:        get("LISTEN", "127.0.0.1:8080"),
		DataDir:       get("DATA_DIR", "data"),
		AdminPassword: get("ADMIN_PASSWORD", ""),
		SMTPHost:      get("SMTP_HOST", ""),
		SMTPUser:      get("SMTP_USER", ""),
		SMTPPass:      get("SMTP_PASS", ""),
		FromName:      get("FROM_NAME", "PaxVelo"),

		PayPalIndividual: get("PAYPAL_INDIVIDUAL_URL", ""),
		PayPalFamily:     get("PAYPAL_FAMILY_URL", ""),
		KitURL:           get("KIT_URL", defaultKitURL),
		StravaURL:        get("STRAVA_URL", "https://www.strava.com/clubs/10197"),
		DonateURL:        get("DONATE_URL", ""),

		StravaClientID:     get("STRAVA_CLIENT_ID", ""),
		StravaClientSecret: get("STRAVA_CLIENT_SECRET", ""),
		StravaAPI:          strings.TrimRight(get("STRAVA_API", "https://www.strava.com"), "/"),
		CalendarAPIKey:     get("CALENDAR_API_KEY", ""),

		InstagramFeeds: strings.TrimRight(get("INSTAGRAM_FEEDS", "https://feeds.behold.so"), "/"),
	}
	c.FromEmail = get("FROM_EMAIL", c.SMTPUser)
	c.AdminEmail = get("ADMIN_EMAIL", c.FromEmail)
	c.RidesEmail = get("RIDES_EMAIL", c.AdminEmail)
	if c.RidesEmail == "" {
		c.RidesEmail = "PaxVeloClub@gmail.com"
	}
	c.StravaClubID = get("STRAVA_CLUB_ID", clubIDFrom(c.StravaURL))

	if c.SMTPPort, err = strconv.Atoi(get("SMTP_PORT", "587")); err != nil {
		return c, fmt.Errorf("SMTP_PORT must be a number: %w", err)
	}
	if c.EmailLimit, err = strconv.Atoi(get("EMAIL_DAILY_LIMIT", "300")); err != nil {
		return c, fmt.Errorf("EMAIL_DAILY_LIMIT must be a number: %w", err)
	}
	if c.SendDelay, err = time.ParseDuration(get("SEND_DELAY", "2s")); err != nil {
		return c, fmt.Errorf("SEND_DELAY must look like 2s or 500ms: %w", err)
	}
	if c.DuesIndividual, err = strconv.Atoi(get("DUES_INDIVIDUAL", "35")); err != nil {
		return c, fmt.Errorf("DUES_INDIVIDUAL must be a whole number: %w", err)
	}
	if c.DuesFamily, err = strconv.Atoi(get("DUES_FAMILY", "45")); err != nil {
		return c, fmt.Errorf("DUES_FAMILY must be a whole number: %w", err)
	}
	if c.Location, err = time.LoadLocation(get("TIMEZONE", "America/New_York")); err != nil {
		return c, fmt.Errorf("TIMEZONE: %w", err)
	}
	if len(c.AdminPassword) < 12 {
		return c, errors.New("set ADMIN_PASSWORD (at least 12 characters) in paxvelo.conf")
	}
	if c.SMTPHost != "" && c.FromEmail == "" {
		return c, errors.New("set FROM_EMAIL (or SMTP_USER) so emails have a sender")
	}
	return c, nil
}

// clubIDFrom finds the club number in a Strava club address, like
// https://www.strava.com/clubs/10197.
func clubIDFrom(u string) string {
	_, rest, ok := strings.Cut(u, "/clubs/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return id
}
