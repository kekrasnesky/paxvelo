// Command paxvelo runs the PaxVelo club website: the public pages, the join
// form, and the admin portal for managing members and emailing them.
//
// Everything the site needs (page text, templates, CSS, fonts, photos) is compiled into
// the program, so deploying means copying one file. Member data lives in a
// single JSON file in DATA_DIR. There are no third-party Go dependencies.
package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // so TIMEZONE works on any server
)

//go:embed web
var webFS embed.FS

// The page text and photos, edited by club volunteers (see content/README.md).
//
//go:embed content
var contentFS embed.FS

type App struct {
	cfg    Config
	store  *Store
	site   *Site
	mailer *Mailer
	limit  *Limiter
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	site, err := newSite(cfg)
	if err != nil {
		log.Fatal(err)
	}
	app := &App{cfg: cfg, store: store, site: site, limit: newLimiter()}
	app.mailer = newMailer(cfg, store, site)
	app.refreshNewsletter()
	app.refreshMinutes()
	if err := app.rebuildContent(); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.mailer.run(ctx)
	go app.stravaSyncLoop(ctx)
	go app.instagramSyncLoop(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Printf("PaxVelo running at http://%s (email: %s)", cfg.Listen, app.mailer.mode())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	// Public pages, rendered once at startup and served from memory.
	for _, path := range a.site.pagePaths {
		if path == "/kit" {
			continue // has its own route below
		}
		pattern := "GET " + path
		if path == "/" {
			pattern = "GET /{$}"
		}
		mux.Handle(pattern, a.site.serveContent(path))
	}
	mux.Handle("GET /static/", a.site.staticFiles("/static/", a.site.static))
	mux.Handle("GET /photos/", a.site.staticFiles("/photos/", a.site.photos))
	mux.Handle("GET /favicon.ico", http.RedirectHandler("/static/img/logo.svg", http.StatusMovedPermanently))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("User-agent: *\nDisallow: /admin\nDisallow: /unsubscribe\nDisallow: /membership\nDisallow: /strava/\nDisallow: /calendar/new\nSitemap: " + a.cfg.SiteURL + "/sitemap.xml\n"))
	})
	mux.HandleFunc("GET /sitemap.xml", a.sitemap)
	// Proves to Bing Webmaster Tools that the club owns the site.
	mux.HandleFunc("GET /BingSiteAuth.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Write([]byte(bingSiteAuth))
	})
	// Old Squarespace addresses, so existing links and bookmarks still land somewhere.
	for _, old := range []string{"/info", "/home", "/cart", "/account", "/insurancewaiver"} {
		mux.Handle("GET "+old, http.RedirectHandler("/", http.StatusMovedPermanently))
	}
	mux.Handle("GET /tnrinfo", http.RedirectHandler("/rides#tuesday-night-ride", http.StatusMovedPermanently))
	mux.Handle("GET /thnr-info", http.RedirectHandler("/rides#thursday-night-ride", http.StatusMovedPermanently))

	// Short, memorable links for emails and flyers: paxvelo.club/kit and /strava.
	// /kit is the kit page (content/kit.md), or straight to the team store if
	// that page is ever removed, so the links already out there keep working.
	mux.HandleFunc("GET /kit", func(w http.ResponseWriter, r *http.Request) {
		if (*a.site.pages.Load())["/kit"] != nil {
			a.site.serveContent("/kit").ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, a.cfg.KitURL, http.StatusFound)
	})
	mux.Handle("GET /strava", http.RedirectHandler(a.cfg.StravaURL, http.StatusFound))

	// The newsletter, and photos uploaded for it in the admin.
	mux.HandleFunc("GET /newsletter", a.newsletterPage)
	mux.HandleFunc("GET /newsletter/{slug}", a.newsletterPage)
	mux.Handle("GET /media/", a.site.staticFiles("/media/", os.DirFS(a.mediaDir())))
	mux.Handle("GET /instagram/", a.site.staticFiles("/instagram/", os.DirFS(a.instaDir())))

	// Meeting minutes, and the files uploaded for them in the admin.
	mux.HandleFunc("GET /minutes", a.minutesPage)
	mux.Handle("GET /files/", a.serveFiles())

	// The ride calendar, and signing in with Strava to lead rides.
	mux.HandleFunc("GET /calendar", a.calendarPage)
	mux.HandleFunc("GET /routes", a.routesPage)
	mux.HandleFunc("GET /calendar/new", a.newRidePage)
	mux.HandleFunc("POST /calendar/new", a.addRide)
	mux.HandleFunc("GET /calendar/ride/{key}", a.ridePage)
	mux.HandleFunc("POST /calendar/ride/{key}/lead", a.leadRide)
	mux.HandleFunc("POST /calendar/ride/{key}/stepdown", a.stepDown)
	mux.HandleFunc("POST /calendar/ride/{key}/strava", a.setRideStrava)
	mux.HandleFunc("POST /calendar/ride/{key}/cancel", a.cancelRide)
	mux.HandleFunc("GET /strava/login", a.stravaLogin)
	mux.HandleFunc("GET /strava/callback", a.stravaCallback)
	mux.HandleFunc("POST /strava/signout", a.stravaSignOut)

	// Joining and leaving.
	mux.HandleFunc("GET /join", a.joinPage)
	mux.HandleFunc("POST /join", a.join)
	mux.HandleFunc("GET /unsubscribe", a.unsubscribePage)
	mux.HandleFunc("POST /unsubscribe", a.unsubscribe)
	mux.HandleFunc("GET /membership", a.membershipPage)
	mux.HandleFunc("POST /membership", a.saveMembership)
	mux.HandleFunc("POST /membership/emails", a.resubscribe)
	mux.HandleFunc("POST /membership/link", a.sendMembershipLink)

	// Admin portal.
	mux.HandleFunc("GET /admin/login", a.loginPage)
	mux.HandleFunc("POST /admin/login", a.login)
	mux.HandleFunc("POST /admin/logout", a.logout)
	mux.HandleFunc("GET /admin/forgot", a.forgotPage)
	mux.HandleFunc("POST /admin/forgot", a.forgot)
	mux.HandleFunc("GET /admin/password", a.passwordPage)
	mux.HandleFunc("POST /admin/password", a.setPassword)
	mux.Handle("GET /admin/admins", a.auth(a.adminsPage))
	mux.Handle("POST /admin/admins", a.auth(a.addAdmin))
	mux.Handle("POST /admin/admins/{id}/link", a.auth(a.sendAdminLink))
	mux.Handle("POST /admin/admins/{id}/remove", a.auth(a.removeAdmin))
	mux.Handle("POST /admin/admins/{id}/president", a.auth(a.makePresident))
	mux.Handle("POST /admin/admins/password", a.auth(a.changePassword))
	mux.Handle("GET /admin", a.auth(a.attentionPage))
	mux.Handle("GET /admin/guide", a.auth(a.guidePage))
	mux.Handle("GET /admin/members", a.auth(a.membersPage))
	mux.Handle("GET /admin/", http.RedirectHandler("/admin", http.StatusSeeOther))
	mux.Handle("POST /admin/members", a.auth(a.addMember))
	mux.Handle("POST /admin/members/{id}/delete", a.auth(a.deleteMember))
	mux.Handle("POST /admin/members/{id}/paid", a.auth(a.markPaid))
	mux.Handle("GET /admin/members.csv", a.auth(a.exportCSV))
	mux.Handle("POST /admin/import", a.auth(a.importCSV))
	mux.Handle("GET /admin/backup", a.auth(a.backup))
	mux.Handle("GET /admin/email", a.auth(a.emailPage))
	mux.Handle("GET /admin/email/status", a.auth(a.emailStatus))
	mux.Handle("POST /admin/email/test", a.auth(a.sendTest))
	mux.Handle("POST /admin/email/preview", a.auth(a.emailPreview))
	mux.Handle("POST /admin/email/send", a.auth(a.sendAll))
	mux.Handle("POST /admin/email/{id}/stop", a.auth(a.stopCampaign))
	mux.Handle("POST /admin/email/{id}/resume", a.auth(a.resumeCampaign))
	mux.Handle("POST /admin/email/discard", a.auth(a.discardDraft))
	mux.Handle("POST /admin/welcome", a.auth(a.saveWelcome))
	mux.Handle("GET /admin/newsletter", a.auth(a.newsletterAdmin))
	mux.Handle("POST /admin/newsletter", a.auth(a.createIssue))
	mux.Handle("GET /admin/newsletter/{id}", a.auth(a.editIssuePage))
	mux.Handle("POST /admin/newsletter/{id}", a.auth(a.saveIssue))
	mux.Handle("GET /admin/newsletter/{id}/preview", a.auth(a.previewIssue))
	mux.Handle("POST /admin/newsletter/{id}/unpublish", a.auth(a.unpublishIssue))
	mux.Handle("POST /admin/newsletter/{id}/delete", a.auth(a.deleteIssue))
	mux.Handle("POST /admin/newsletter/{id}/copy", a.auth(a.copyIssue))
	mux.Handle("POST /admin/newsletter/photos", a.auth(a.uploadNewsPhotos))
	mux.Handle("GET /admin/rides", a.auth(a.ridesAdmin))
	mux.Handle("POST /admin/strava/connect", a.auth(a.stravaConnect))
	mux.Handle("GET /admin/strava/callback", a.auth(a.stravaConnectCallback))
	mux.Handle("POST /admin/strava/disconnect", a.auth(a.stravaDisconnect))
	mux.Handle("POST /admin/strava/sync", a.auth(a.stravaSyncNow))

	// The calendar API, for scripts (see README.md, "Calendar API").
	mux.HandleFunc("GET /api/rides", a.apiRides)
	mux.HandleFunc("PUT /api/rides/{id}", a.apiPutRide)
	mux.HandleFunc("DELETE /api/rides/{id}", a.apiDeleteRide)
	mux.HandleFunc("POST /api/strava/sync", a.apiStravaSync)
	mux.Handle("GET /admin/calendar", a.auth(a.calendarAdmin))
	mux.Handle("GET /admin/history", a.auth(a.historyAdmin))
	mux.Handle("GET /admin/rides/new", a.auth(a.newRideAdminPage))
	mux.Handle("POST /admin/rides/new", a.auth(a.addRideAdmin))
	mux.Handle("GET /admin/rides/{key}", a.auth(a.rideEditPage))
	mux.Handle("POST /admin/rides/{key}", a.auth(a.saveRideAdmin))
	mux.Handle("POST /admin/rides/{key}/cancel", a.auth(a.cancelRideAdmin))
	mux.Handle("POST /admin/rides/{key}/approve", a.auth(a.approveRide))
	mux.Handle("POST /admin/rides/{key}/delete", a.auth(a.deleteRideAdmin))
	mux.Handle("POST /admin/weekly", a.auth(a.addSeries))
	mux.Handle("GET /admin/weekly/{id}", a.auth(a.seriesEditPage))
	mux.Handle("POST /admin/weekly/{id}", a.auth(a.saveSeries))
	mux.Handle("POST /admin/weekly/{id}/delete", a.auth(a.deleteSeries))
	mux.Handle("GET /admin/pages", a.auth(a.pagesAdmin))
	mux.Handle("POST /admin/pages/photo", a.auth(a.pagePhoto))
	mux.Handle("POST /admin/pages", a.auth(a.createPage))
	mux.Handle("GET /admin/pages/{file}", a.auth(a.editPagePage))
	mux.Handle("POST /admin/pages/{file}/delete", a.auth(a.deletePage))
	mux.Handle("POST /admin/pages/{file}", a.auth(a.savePage))
	mux.Handle("POST /admin/pages/{file}/reset", a.auth(a.resetPage))
	mux.Handle("GET /admin/board", a.auth(a.boardAdmin))
	mux.Handle("POST /admin/board", a.auth(a.saveBoard))
	mux.Handle("GET /admin/home-photos", a.auth(a.homePhotosAdmin))
	mux.Handle("POST /admin/home-photos", a.auth(a.saveHomePhotos))
	mux.Handle("GET /admin/instagram", a.auth(a.instagramAdmin))
	mux.Handle("POST /admin/instagram", a.auth(a.saveInstagram))
	mux.Handle("POST /admin/instagram/connect", a.auth(a.instagramConnect))
	mux.Handle("POST /admin/instagram/disconnect", a.auth(a.instagramDisconnect))
	mux.Handle("POST /admin/instagram/sync", a.auth(a.instagramSyncNow))
	mux.Handle("GET /admin/minutes", a.auth(a.minutesAdmin))
	mux.Handle("POST /admin/minutes", a.auth(a.addMinutes))
	mux.Handle("POST /admin/minutes/{id}/publish", a.auth(a.publishMinutes))
	mux.Handle("POST /admin/minutes/{id}/delete", a.auth(a.deleteMinutes))
	mux.Handle("POST /admin/newsletter/{id}/email", a.auth(a.emailIssue))

	mux.HandleFunc("/", a.site.serveAny)
	return secureHeaders(mux)
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
