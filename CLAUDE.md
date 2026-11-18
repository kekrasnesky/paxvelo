# PaxVelo site: technical notes

Single Go program (stdlib only, no DB, no third-party code). Pages, templates, CSS, fonts and images are embedded in one binary (`web/`, `content/`). State lives in `data/paxvelo.json` (written on every change); uploads in `data/media`, minutes in `data/files`, receipts in `data/receipts`, Instagram cache in `data/instagram`, stats in `data/stats.json` (counts only, not backed up). Board-facing docs are in README.md; keep it short and non-technical.

**Detailed notes are in `docs/`. Before changing a feature, read its section there, and update it when behaviour changes.** Keep this file to what applies everywhere.

## Run / test
- Go 1.22+. `cp paxvelo.conf.example paxvelo.conf`, set `ADMIN_PASSWORD` (12+ chars), leave `SMTP_HOST` empty (emails go to the log, admin password links show on screen). `go run .` → http://localhost:8080, admin at `/admin`. `go test ./...` is what CI runs; also `gofmt -l .`.
- Previews: never point one at a data folder with real members. Use a scratch conf with its own empty `DATA_DIR`, `LISTEN=127.0.0.1:<port>` (not `:<port>`: Windows Firewall asks every run), empty `SMTP_HOST`, and run `go run . -demo`: it fills the folder with a made-up club (`demo.go`: members in every state, rides incl. waiting/virtual/community/event, riders, an RSVP, a loaner request, treasurer lines, a spending request; all `@example.com`) and refuses real data, `SMTP_HOST` or a non-local `SITE_URL`. `TestDemoClubAdminPages` opens every admin screen with it at all three admin levels; add a new screen there.

## Rules for every change
- **No explaining (Karl's rule), on every page the site builds**, public or admin (forms, tools, calendar, membership, subscribe…): a title, labels, buttons, data and short tooltips (`title`); no lead paragraphs about how a feature works, no muted footnotes, no "how it works" sections. If it needs words to make sense, change the design. Only real alerts (errors, things needing attention) and short empty states. The club's own written pages (`content/*.md`) are not covered; admin explanations live only on How the site works.
- **Look:** use the tokens on `:root` in site.css / admin.css (`--muted`, `--field`, `--tint`, `--ok|err|warn` with `-bg`/`-ink`, `--r-sm`/`--r-md`/`--radius`/`--pill`, `--shadow-*`, `--ring`), never typed-out colours; never a `--r` token (focus.go's framing clip uses it). Admin grid columns are `minmax(0,…fr)` and fixed minimums `minmax(min(100%,…),…)` (`TestAdminGridsShrink`), so nothing runs off a 320px phone. Lay admin pages out with the building blocks (`.card .box .field .field-row .facts .rows .act-row .wrap-row`) before adding a one-off class. Details: docs/look.md.
- **Admin levels** (`permissions.go`): website admin, board (`a.boardOnly`), president (`a.president`), treasurer (`a.treasurerOnly`). A new `/admin` route on plain `a.auth` must be listed in `websiteAdminRoutes` or `TestAdminRoutesHaveALevel` fails. Details: docs/members.md (Admins).
- **Privacy by design:** stats are counts only; nothing ties a phone, rider cookie or alert email to a member unless the feature says so; the site never posts to an address a visitor picked; public forms get honeypot + form token + a per-IP limit; links that act (confirm, approve) do it on POST, never GET (email scanners open links).
- **Legal text:** `web/waiver.html` and `web/waiver-guardian.html` are the League of American Bicyclists' waiver (change only for a new League version); `web/waiver-loaner.html` is the club's (insurer or lawyer approves changes); signed waivers keep their original text.
- **Commits:** none unless Karl asks; when asked, push to `main` (CI deploys).

## Where things are
- Go: `main.go`, `site.go`, `public.go` (routes), `store.go` (data file), `config.go` (conf keys), `admin*.go`, `appsearch.go`, `membership.go`/`waivers.go`/`ridewaivers.go`/`household.go`, `rides*.go`/`calendar.go`/`ridestarts.go`/`levels.go`/`api.go`/`loaners.go`, `strava*.go`/`history.go`, `newsletter.go`, `gallery*.go`/`sharephotos.go`/`photo.go`, `instagram.go`/`instafeed.go`, `mail.go`/`emailfmt.go`/`emailcount.go`/`summary.go`, `stats.go`, `clubpin.go`, `minutes.go`, `rsvp*.go`, `treasurer.go`/`treasureradmin.go`/`boardroles.go`/`requests.go`/`requestsadmin.go`/`requestguess.go`/`requestanswer.go`/`requestreminders.go`.
- Templates `web/templates/*.html` (admin: `admin.html`, `admin-email.html`, `admin-treasurer.html`, `admin-kit.html`, `admin-rsvp.html`, `admin-app` in admin.html); `web/static/site.css`, `admin.css`, `site.js` (also the stats beacon), `sw.js`; `content/<page>.md` = pages (formatting: `content/README.md`).
- Config keys (all in `paxvelo.conf`, see example): `SITE_URL ADMIN_PASSWORD DUES_INDIVIDUAL DUES_FAMILY PAYPAL_INDIVIDUAL_URL PAYPAL_FAMILY_URL KIT_URL STRAVA_URL DONATE_URL STRAVA_CLIENT_ID STRAVA_CLIENT_SECRET RIDES_EMAIL CALENDAR_API_KEY SMTP_* FROM_NAME FROM_EMAIL REPLY_TO SEND_DELAY EMAIL_DAILY_LIMIT LISTEN DATA_DIR TIMEZONE BACKUP_DIR`.

## docs/
- **look.md**: site.css look (fonts, spacing, home page bands, page widths, computer layouts), templates and static files, admin.css look (bar, buttons, feedback, phones, `stack`/`cards` tables), drag bars, CSP hashes.
- **website.md**: content pages, card editor and page editor, uploads, photo framing, home video and photos, member photos and gallery/albums, minutes, Instagram, the public home screen app and service worker, the admin app and admin alerts, stats.
- **rides.md**: rides/events/virtual/community rides, calendar feed, ride status, routes, forecast, QR codes, I'm in, ride alerts (push and email), ride waivers, loaner bikes, Strava import.
- **members.md**: admins and board roles, member page, family riders, email choices, renewing, dues and standing, site-move email, club kit, RSVPs.
- **email.md**: Brevo SMTP and banner, emailing an issue, automations, sent log.
- **money.md**: treasurer books and reports, spending requests.
- **ops.md**: calendar API, deploy (Cloudflare, CI, SSH), backups and restore, Squarespace import.
