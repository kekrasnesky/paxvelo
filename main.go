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
	stats  *Stats
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
	stats, err := openStats(cfg.DataDir, store.Secret(), cfg.Location)
	if err != nil {
		log.Fatal(err)
	}
	app := &App{cfg: cfg, store: store, site: site, limit: newLimiter(), stats: stats}
	app.mailer = newMailer(cfg, store, site)
	app.mailer.stats = stats
	app.refreshNewsletter()
	app.ensureHeroPhone()
	// The weekly media backup (deploy/setup.sh) copies this folder, so it has to exist.
	if err := os.MkdirAll(app.receiptsDir(), 0o700); err != nil {
		log.Printf("receipts folder: %v", err)
	}
	if err := app.rebuildContent(); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.mailer.run(ctx)
	go app.stravaSyncLoop(ctx)
	go app.instagramSyncLoop(ctx)
	go app.stats.run(ctx)
	go app.upcomingLoop(ctx)
	go app.automationLoop(ctx)
	go app.sizeOldPhotos()
	go app.convertOldMinutes() // Word minutes from before LibreOffice was installed

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
	if err := app.stats.Save(); err != nil { // the last minute's counts
		log.Printf("saving stats: %v", err)
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
	mux.Handle("GET /favicon.ico", http.RedirectHandler("/static/img/favicon.svg", http.StatusMovedPermanently))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("User-agent: *\nDisallow: /admin\nDisallow: /unsubscribe\nDisallow: /membership\nDisallow: /strava/\nDisallow: /calendar/new\nDisallow: /e/\nSitemap: " + a.cfg.SiteURL + "/sitemap.xml\n"))
	})
	mux.HandleFunc("GET /sitemap.xml", a.sitemap)
	// Proves to Bing Webmaster Tools that the club owns the site.
	mux.HandleFunc("GET /BingSiteAuth.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Write([]byte(bingSiteAuth))
	})
	// Old Squarespace addresses, so existing links and bookmarks still land somewhere.
	for _, old := range []string{"/info", "/home", "/cart", "/account"} {
		mux.Handle("GET "+old, http.RedirectHandler("/", http.StatusMovedPermanently))
	}
	mux.Handle("GET /insurancewaiver", http.RedirectHandler("/membership", http.StatusMovedPermanently)) // the waiver is signed there now
	mux.Handle("GET /tnrinfo", http.RedirectHandler("/rides#tuesday-night-ride", http.StatusMovedPermanently))
	mux.Handle("GET /thnr-info", http.RedirectHandler("/rides#thursday-night-ride", http.StatusMovedPermanently))

	// Short, memorable links for emails and flyers: paxvelo.club/kit and /strava.
	// /kit is the kit page (content/kit.md), or straight to the team store if
	// that page is ever removed, so the links already out there keep working.
	// It's also the club kit store (kitpage.go).
	mux.HandleFunc("GET /kit", a.kitPage)
	mux.HandleFunc("POST /kit/order", a.kitOrder)
	mux.Handle("GET /strava", http.RedirectHandler(a.cfg.StravaURL, http.StatusFound))

	// The newsletter, and photos uploaded for it in the admin.
	mux.HandleFunc("GET /newsletter", a.newsletterPage)
	mux.HandleFunc("GET /newsletter/{slug}", a.newsletterPage)
	mux.Handle("GET /media/", a.site.staticFiles("/media/", os.DirFS(a.mediaDir())))
	mux.Handle("GET /instagram/", a.site.staticFiles("/instagram/", os.DirFS(a.instaDir())))

	// Visit and email counts for the Stats screen (stats.go).
	mux.HandleFunc("POST /admin/count", a.countView)
	mux.HandleFunc("GET /e/o/{c}/{code}/{sig}", a.emailOpened)
	mux.HandleFunc("GET /e/c/{c}/{code}/{sig}", a.emailClicked)

	// Meeting minutes: the list (/minutes) and the files. Anyone can open
	// them; search engines are told not to index them.
	mux.HandleFunc("GET /minutes", a.minutesPage)
	mux.Handle("GET /files/", a.serveFiles())

	// The ride calendar, and signing in with Strava to lead rides.
	mux.HandleFunc("GET /calendar", a.calendarPage)
	mux.HandleFunc("GET /routes", a.routesPage)
	mux.HandleFunc("GET /calendar/new", a.newRidePage)
	mux.HandleFunc("POST /calendar/new", a.addRide)
	mux.HandleFunc("GET /calendar/ride/{key}", a.ridePage)
	mux.HandleFunc("GET /calendar/ride/{key}/calendar.ics", a.rideICS)
	mux.HandleFunc("POST /calendar/ride/{key}/lead", a.leadRide)
	mux.HandleFunc("POST /calendar/ride/{key}/stepdown", a.stepDown)
	mux.HandleFunc("POST /calendar/ride/{key}/strava", a.setRideStrava)
	mux.HandleFunc("POST /calendar/ride/{key}/cancel", a.cancelRide)

	// Joining and leaving.
	mux.HandleFunc("GET /join", a.joinPage)
	mux.HandleFunc("POST /join", a.join)
	mux.HandleFunc("GET /unsubscribe", a.unsubscribePage)
	mux.HandleFunc("POST /unsubscribe", a.unsubscribe)
	mux.HandleFunc("GET /membership", a.membershipPage)
	mux.HandleFunc("POST /membership", a.saveMembership)
	mux.HandleFunc("POST /membership/emails", a.resubscribe)
	mux.HandleFunc("POST /membership/confirm", a.confirmEmail)
	mux.HandleFunc("POST /membership/link", a.sendMembershipLink)
	mux.HandleFunc("POST /membership/forget", a.forgetMember)
	mux.HandleFunc("POST /membership/waiver", a.signWaiver)
	mux.HandleFunc("POST /membership/waiver/minor", a.signMinorWaiver)
	mux.HandleFunc("POST /membership/household", a.saveHousehold)
	mux.HandleFunc("GET "+rideWaiverPath, a.rideWaiverPage)
	mux.HandleFunc("POST "+rideWaiverPath, a.signRideWaiver)
	mux.Handle("GET /loaner", http.RedirectHandler(rideWaiverPath+"?bike="+bikeLoaner, http.StatusFound))
	mux.HandleFunc("GET /calendar/ride/{key}/loaner", a.loanerForm)
	mux.HandleFunc("POST /calendar/ride/{key}/loaner", a.askLoaner)
	mux.HandleFunc("GET /share-photos", a.sharePhotosPage)
	mux.HandleFunc("POST /share-photos", a.sharePhotos)
	mux.HandleFunc("GET /gallery", a.galleryPage)
	mux.HandleFunc("GET /gallery/{album}", a.albumPage)
	mux.HandleFunc("GET /gallery/{album}/{photo}", a.photoPage)

	// Admin portal.
	mux.HandleFunc("GET /admin/login", a.loginPage)
	mux.HandleFunc("POST /admin/login", a.login)
	mux.HandleFunc("POST /admin/logout", a.logout)
	mux.HandleFunc("GET /admin/forgot", a.forgotPage)
	mux.HandleFunc("POST /admin/forgot", a.forgot)
	mux.HandleFunc("GET /admin/password", a.passwordPage)
	mux.HandleFunc("POST /admin/password", a.setPassword)
	mux.Handle("GET /admin/admins", a.auth(a.adminsPage))
	mux.Handle("POST /admin/admins", a.president("/admin/admins", a.addAdmin))
	mux.Handle("POST /admin/admins/{id}/link", a.president("/admin/admins", a.sendAdminLink))
	mux.Handle("POST /admin/admins/{id}/remove", a.president("/admin/admins", a.removeAdmin))
	mux.Handle("POST /admin/admins/{id}/president", a.auth(a.makePresident))
	mux.Handle("POST /admin/admins/password", a.auth(a.changePassword))
	mux.Handle("POST /admin/admins/paypal", a.auth(a.setPayPal))
	mux.Handle("GET /admin/security", a.boardOnly(a.securityPage))
	mux.Handle("POST /admin/security", a.boardOnly(a.securityAction))
	mux.Handle("GET /admin", a.auth(a.attentionPage))
	mux.Handle("GET /admin/guide", a.auth(a.guidePage))
	mux.Handle("POST /admin/intro", a.auth(a.dismissIntro))
	mux.Handle("GET /admin/members", a.auth(a.membersPage))
	mux.Handle("GET /admin/stats", a.auth(a.statsAdmin))
	// Treasurer tab (treasureradmin.go): board members only. They add and
	// edit; only the treasurer or the president removes.
	mux.Handle("GET /admin/treasurer", a.boardOnly(a.expensesPage))
	mux.Handle("GET /admin/treasurer/income", a.boardOnly(a.incomePage))
	mux.Handle("POST /admin/treasurer/income/dues", a.boardOnly(a.duesCategory))
	mux.Handle("POST /admin/treasurer/income/fee", a.boardOnly(a.payPalFee))
	mux.Handle("POST /admin/treasurer/entries", a.boardOnly(a.addLedger))
	mux.Handle("GET /admin/treasurer/entries/{id}", a.boardOnly(a.editLedgerPage))
	mux.Handle("POST /admin/treasurer/entries/{id}", a.boardOnly(a.saveLedger))
	mux.Handle("POST /admin/treasurer/entries/{id}/remove", a.treasurerOnly("/admin/treasurer", a.removeLedger))
	mux.Handle("POST /admin/treasurer/entries/{id}/restore", a.treasurerOnly("/admin/treasurer", a.restoreLedger))
	mux.Handle("GET /admin/treasurer/receipts/{name}", a.auth(a.receiptFile))
	// Expense reports and purchase requests (requestsadmin.go): any board
	// member sends one; the treasurer and the president answer.
	mux.Handle("GET /admin/treasurer/requests", a.auth(a.requestsPage))
	mux.Handle("POST /admin/treasurer/requests", a.auth(a.sendRequest))
	mux.Handle("POST /admin/treasurer/requests/{id}/approve", a.boardOnly(a.decideRequest(true)))
	mux.Handle("POST /admin/treasurer/requests/{id}/reject", a.boardOnly(a.decideRequest(false)))
	mux.Handle("POST /admin/treasurer/requests/{id}/withdraw", a.auth(a.withdrawRequest))
	mux.Handle("POST /admin/treasurer/requests/{id}/restore", a.auth(a.restoreRequest))
	mux.Handle("POST /admin/treasurer/requests/{id}/bought", a.auth(a.boughtRequest))
	mux.Handle("POST /admin/treasurer/requests/{id}/paid", a.boardOnly(a.paidBackRequest))
	mux.Handle("POST /admin/treasurer/requests/{id}/category", a.boardOnly(a.categoryRequest))
	mux.Handle("POST /admin/treasurer/budget/one-ok", a.boardOnly(a.oneLimit))
	mux.Handle("GET /admin/treasurer/requests/guess", a.boardOnly(a.guessRequest))
	// Approve or Reject from the email (requestanswer.go): no login, the
	// link is signed for one approver; only the page's button answers.
	mux.Handle("GET /request-answer/{token}", http.HandlerFunc(a.answerPage))
	mux.Handle("POST /request-answer/{token}", http.HandlerFunc(a.answerPost))
	mux.Handle("GET /request-answer/{token}/receipt", http.HandlerFunc(a.answerReceipt))
	mux.Handle("GET /admin/treasurer/budget", a.boardOnly(a.budgetPage))
	mux.Handle("POST /admin/treasurer/budget", a.boardOnly(a.saveBudget))
	mux.Handle("GET /admin/treasurer/reports", a.boardOnly(a.reportsPage))
	mux.Handle("POST /admin/treasurer/reports", a.boardOnly(a.saveReport))
	mux.Handle("GET /admin/treasurer/report.csv", a.boardOnly(a.reportDownload))
	mux.Handle("GET /admin/treasurer/report.md", a.boardOnly(a.reportDownload))
	mux.Handle("POST /admin/treasurer/reports/{id}/remove", a.treasurerOnly("/admin/treasurer/reports", a.removeReport))
	mux.Handle("POST /admin/treasurer/reports/{id}/restore", a.treasurerOnly("/admin/treasurer/reports", a.restoreReport))
	mux.Handle("GET /admin/", http.RedirectHandler("/admin", http.StatusSeeOther))
	mux.Handle("POST /admin/members", a.boardOnly(a.addMember))
	mux.Handle("GET /admin/members/{id}", a.auth(a.memberPage))
	mux.Handle("POST /admin/members/{id}", a.boardOnly(a.saveMember))
	mux.Handle("POST /admin/members/paid", a.boardOnly(a.markPaidMany))
	mux.Handle("POST /admin/members/{id}/delete", a.boardOnly(a.deleteMember))
	mux.Handle("POST /admin/members/{id}/paid", a.boardOnly(a.markPaid))
	mux.Handle("POST /admin/members/delete", a.boardOnly(a.deleteMembers))
	mux.Handle("POST /admin/members/moved", a.boardOnly(a.movedMany))
	mux.Handle("POST /admin/moved", a.boardOnly(a.saveMoved))
	mux.Handle("POST /admin/members/{id}/record", a.boardOnly(a.saveRecord))
	mux.Handle("GET /admin/members.csv", a.boardOnly(a.exportCSV))
	mux.Handle("GET /admin/waivers", a.boardOnly(a.waiversPage))
	mux.Handle("GET /admin/waivers.csv", a.boardOnly(a.exportWaivers))
	mux.Handle("GET /admin/waivers/{id}", a.boardOnly(a.waiverPage))
	mux.Handle("GET /admin/waivers/{id}/signature.png", a.boardOnly(a.waiverSignature))
	mux.Handle("POST /admin/waivers/{id}/delete", a.boardOnly(a.deleteWaiver))
	mux.Handle("GET /admin/ride-waivers.csv", a.boardOnly(a.exportRideWaivers))
	mux.Handle("GET /admin/loaners", a.auth(a.loanersPage))
	mux.Handle("POST /admin/loaners/bikes", a.auth(a.saveLoanerBike))
	mux.Handle("POST /admin/loaners/bikes/{id}/remove", a.auth(a.removeLoanerBike))
	mux.Handle("POST /admin/loaners/{id}/confirm", a.auth(a.answerLoaner(loanConfirmed)))
	mux.Handle("POST /admin/loaners/{id}/decline", a.auth(a.answerLoaner(loanDeclined)))
	mux.Handle("POST /admin/loaners/{id}/cancel", a.auth(a.answerLoaner(loanCancelled)))
	mux.Handle("GET /admin/ride-waivers/{id}", a.boardOnly(a.adminRideWaiver))
	mux.Handle("GET /admin/ride-waivers/{id}/signature.png", a.boardOnly(a.rideWaiverSignature))
	mux.Handle("POST /admin/ride-waivers/{id}/delete", a.boardOnly(a.deleteRideWaiver))
	mux.Handle("POST /admin/import", a.boardOnly(a.importCSV))
	mux.Handle("GET /admin/backup", a.auth(a.backup))
	mux.Handle("GET /admin/email", a.auth(a.emailPage))
	mux.Handle("GET /admin/email/status", a.auth(a.emailStatus))
	mux.Handle("POST /admin/email/test", a.auth(a.sendTest))
	mux.Handle("POST /admin/email/preview", a.auth(a.emailPreview))
	mux.Handle("POST /admin/email/send", a.auth(a.sendAll))
	mux.Handle("POST /admin/email/{id}/stop", a.auth(a.stopCampaign))
	mux.Handle("POST /admin/email/{id}/resume", a.auth(a.resumeCampaign))
	mux.Handle("POST /admin/email/discard", a.auth(a.discardDraft))
	mux.Handle("POST /admin/email/draft", a.auth(a.saveDraft))
	mux.Handle("GET /admin/email/log", a.boardOnly(a.sentLogPage))
	mux.Handle("GET /admin/email/auto", a.boardOnly(a.autoPage))
	mux.Handle("GET /admin/email/auto/{key}", a.boardOnly(a.autoEditPage))
	mux.Handle("POST /admin/email/auto/{key}/save", a.boardOnly(a.autoSave))
	mux.Handle("GET /admin/email/auto/{key}/preview", a.boardOnly(a.autoPreview))
	mux.Handle("POST /admin/email/auto/{key}/preview", a.boardOnly(a.autoPreview))
	mux.Handle("POST /admin/email/auto/{key}/test", a.boardOnly(a.autoTest))
	mux.Handle("POST /admin/email/auto/{key}/pause", a.boardOnly(a.autoPause))
	mux.Handle("POST /admin/email/auto/{key}/remove", a.boardOnly(a.autoRemove))
	mux.Handle("POST /admin/email/auto/{key}/restore", a.boardOnly(a.autoRestore))
	mux.Handle("GET /admin/newsletter/{id}/email-preview", a.boardOnly(a.issueEmailPreview))
	mux.Handle("POST /admin/newsletter/{id}/email-test", a.boardOnly(a.testIssueEmail))
	mux.Handle("POST /admin/newsletter/{id}/email-send", a.boardOnly(a.sendIssueEmail))
	mux.Handle("POST /admin/welcome", a.boardOnly(a.saveWelcome))
	mux.Handle("GET /admin/newsletter", a.boardOnly(a.newsletterAdmin))
	mux.Handle("POST /admin/newsletter", a.boardOnly(a.createIssue))
	mux.Handle("GET /admin/newsletter/{id}", a.boardOnly(a.editIssuePage))
	mux.Handle("POST /admin/newsletter/{id}", a.boardOnly(a.saveIssue))
	mux.Handle("GET /admin/newsletter/{id}/preview", a.boardOnly(a.previewIssue))
	mux.Handle("POST /admin/newsletter/{id}/unpublish", a.boardOnly(a.unpublishIssue))
	mux.Handle("POST /admin/newsletter/{id}/delete", a.boardOnly(a.deleteIssue))
	mux.Handle("POST /admin/newsletter/{id}/copy", a.boardOnly(a.copyIssue))
	mux.Handle("POST /admin/newsletter/photos", a.boardOnly(a.uploadNewsPhotos))
	mux.Handle("POST /admin/newsletter/from-gallery", a.boardOnly(a.galleryPicked))
	mux.Handle("GET /admin/submitted", a.auth(a.submittedPage))
	mux.Handle("POST /admin/submitted", a.auth(a.submittedAction))
	mux.Handle("GET /admin/submitted/{id}/photo", a.auth(a.submittedPhoto))
	mux.Handle("GET /admin/gallery/pick", a.boardOnly(a.galleryPick))
	mux.Handle("GET /admin/gallery", a.auth(a.galleryAdminPage))
	mux.Handle("POST /admin/gallery", a.auth(a.galleryAction))
	mux.Handle("POST /admin/gallery/upload", a.auth(a.galleryUpload))
	mux.Handle("GET /admin/gallery/{id}", a.auth(a.albumAdminPage))
	mux.Handle("POST /admin/gallery/{id}", a.auth(a.albumAction))
	mux.Handle("GET /admin/rides", a.auth(a.ridesAdmin))
	mux.Handle("POST /admin/strava/connect", a.boardOnly(a.stravaConnect))
	mux.Handle("GET /admin/strava/callback", a.boardOnly(a.stravaConnectCallback))
	mux.Handle("POST /admin/strava/disconnect", a.boardOnly(a.stravaDisconnect))
	mux.Handle("POST /admin/strava/sync", a.auth(a.stravaSyncNow))
	mux.Handle("GET /admin/strava/preview", a.auth(a.stravaPreviewJSON))

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
	mux.Handle("POST /admin/rides/{key}/newsletter", a.boardOnly(a.eventToNewsletter))
	mux.Handle("POST /admin/weekly", a.auth(a.addSeries))
	mux.Handle("GET /admin/weekly/{id}", a.auth(a.seriesEditPage))
	mux.Handle("POST /admin/weekly/{id}", a.auth(a.saveSeries))
	mux.Handle("POST /admin/weekly/{id}/delete", a.auth(a.deleteSeries))
	mux.Handle("GET /admin/pages", a.boardOnly(a.pagesAdmin))
	mux.Handle("POST /admin/pages/photo", a.boardOnly(a.pagePhoto))
	mux.Handle("POST /admin/pages", a.boardOnly(a.createPage))
	mux.Handle("GET /admin/pages/{file}", a.boardOnly(a.editPagePage))
	mux.Handle("POST /admin/pages/{file}/delete", a.boardOnly(a.deletePage))
	mux.Handle("POST /admin/pages/{file}", a.boardOnly(a.savePage))
	mux.Handle("POST /admin/pages/{file}/reset", a.boardOnly(a.resetPage))
	mux.Handle("POST /admin/pages/{file}/preview", a.boardOnly(a.previewPage))
	mux.Handle("GET /admin/board", a.boardOnly(a.boardAdmin))
	mux.Handle("POST /admin/board", a.president("/admin/board", a.saveBoard))
	mux.Handle("GET /admin/home-photos", a.boardOnly(a.homePhotosAdmin))
	mux.Handle("POST /admin/home-hero", a.boardOnly(a.saveHero))
	mux.Handle("POST /admin/home-photos", a.boardOnly(a.saveHomePhotos))
	mux.Handle("GET /admin/instagram", a.boardOnly(a.instagramAdmin))
	mux.Handle("POST /admin/instagram", a.boardOnly(a.saveInstagram))
	mux.Handle("POST /admin/instagram/connect", a.boardOnly(a.instagramConnect))
	mux.Handle("POST /admin/instagram/disconnect", a.boardOnly(a.instagramDisconnect))
	mux.Handle("POST /admin/instagram/sync", a.boardOnly(a.instagramSyncNow))
	mux.Handle("GET /admin/kit", a.boardOnly(a.kitAdmin))
	mux.Handle("GET /admin/kit/orders.csv", a.boardOnly(a.kitCSV))
	mux.Handle("POST /admin/kit/shop", a.boardOnly(a.saveKitShop))
	mux.Handle("POST /admin/kit/round", a.boardOnly(a.newKitRound))
	mux.Handle("POST /admin/kit/email", a.boardOnly(a.kitEmail))
	mux.Handle("POST /admin/kit/items", a.boardOnly(a.saveKitItem))
	mux.Handle("POST /admin/kit/items/restore", a.boardOnly(a.restoreKitItem))
	mux.Handle("POST /admin/kit/items/{id}/move", a.boardOnly(a.moveKitItem))
	mux.Handle("POST /admin/kit/items/{id}/remove", a.boardOnly(a.removeKitItem))
	mux.Handle("POST /admin/kit/orders/{id}/paid", a.boardOnly(a.kitPaid))
	mux.Handle("GET /admin/minutes", a.auth(a.minutesAdmin))
	mux.Handle("POST /admin/minutes", a.boardOnly(a.addMinutes))
	mux.Handle("POST /admin/minutes/{id}/delete", a.boardOnly(a.deleteMinutes))
	mux.Handle("POST /admin/newsletter/{id}/email", a.boardOnly(a.emailIssue))

	mux.HandleFunc("/", a.site.serveAny)
	return secureHeaders(mux, a.site.csp)
}

// secureHeaders adds the browser security headers. csp is the public pages'
// Content-Security-Policy (csp.go).
func secureHeaders(next http.Handler, csp string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		if p := cspFor(r.URL.Path, csp); p != "" {
			h.Set("Content-Security-Policy", p)
		}
		next.ServeHTTP(w, r)
	})
}
