# PaxVelo site: technical notes

Single Go program (stdlib only, no DB, no third-party code). Pages, templates, CSS, fonts and images are embedded in one binary. State lives in `data/paxvelo.json` (written on every change); uploads in `data/media`, minutes in `data/files`, Instagram cache in `data/instagram`, stats in `data/stats.json` (counts only, saved each minute, not backed up). Board-facing docs are in README.md; keep it short and non-technical.

## Run / test
- Go 1.22+. `cp paxvelo.conf.example paxvelo.conf`, set `ADMIN_PASSWORD` (12+ chars), leave `SMTP_HOST` empty (emails go to the log, admin password links show on screen). `go run .` → http://localhost:8080, admin at `/admin`. `go test ./...` is what CI runs.

## Layout
- Go: `main.go`, `site.go`, `public.go` (routes), `store.go` (data file), `config.go` (conf keys), `admin*.go`, `membership.go`/`waivers.go`/`household.go`, `rides*.go`/`calendar.go`/`levels.go`/`api.go`, `strava*.go`/`routegroups.go`/`history.go`, `newsletter.go`, `gallery*.go`/`sharephotos.go`/`photo.go`, `instagram.go`/`instafeed.go`, `mail.go`/`emailfmt.go`/`emailcount.go`, `stats.go`, `clubpin.go`, `minutes.go`.
- `content/<page>.md` = pages (add a file = add a page; `##` headings get anchors). Admin edits override the GitHub version (revert per page). Formatting: `content/README.md`. Placeholders: `{strava}`, `{kit}`, `{pay_link}`.
- `web/templates/*.html`, `web/static/site.css` (colors at top), `site.js` (also the stats beacon), `web/static/img/` (logo, favicon, og). `web/waiver*.html` are legal text copied from the League of American Bicyclists: change only for a new League version; signed waivers keep their original text (`waiver_texts`).
- Photos: `content/photos/hero.webp` (~2000px, plus square `hero-phone.webp`), `ride.webp`, `giving.webp` (~1000px).
- Config keys (all in `paxvelo.conf`, see example): `SITE_URL ADMIN_PASSWORD DUES_INDIVIDUAL DUES_FAMILY PAYPAL_INDIVIDUAL_URL PAYPAL_FAMILY_URL KIT_URL STRAVA_URL DONATE_URL STRAVA_CLIENT_ID STRAVA_CLIENT_SECRET RIDES_EMAIL CALENDAR_API_KEY SMTP_* FROM_NAME FROM_EMAIL SEND_DELAY EMAIL_DAILY_LIMIT LISTEN DATA_DIR TIMEZONE`.

## Behaviors worth knowing
- **Admins:** email + password (PBKDF2). Setup password `ADMIN_PASSWORD` works by name until the first admin sets a password; afterwards it is only an emergency key (logged as `admin login with the setup password as …`). One President can remove admins/delete members/download backups. Sessions last 30 days; removing an admin or changing a password logs them out.
- **Uploads:** JPG/PNG resized to ≤2000px, EXIF stripped, saved as JPG (PNG with alpha stays PNG); WebP/GIF kept. Max 8 MB. Gallery keeps ≤1600px plus 960/480 copies.
- **Member photos** (`/share-photos`): spam checks (honeypot + form token), 5 sends/hour/IP, queue cap 300, stored in `data/submitted`, reviewed at Email → Sent-in photos. **Club PIN** (Security → Club PIN & access) skips review for photos and ride submissions; 10 tries/hour/IP; also lets ride leaders step down/cancel.
- **Rides:** weekly rides, one-offs, event types, levels (Social/Intermediate/Elite), pace groups with leaders. No rider sign-in. New rides without the PIN wait for approval. Ride emails go to `RIDES_EMAIL` (default PaxVeloClub@gmail.com). Public API: `GET /api/rides?from=&to=`; `PUT/DELETE /api/rides/<id>` and `POST /api/strava/sync` need `Authorization: Bearer $CALENDAR_API_KEY`.
- **Strava import:** admin Rides → Connect Strava (needs a Strava API app: callback domain = site host, one athlete, paid subscription on the owning account). Hourly sync of club events (reads them like Strava's apps do, not a documented API), daily past-events and Club Routes (`/routes`) imports, History page for admins. Private events hide notes/street-address starts from public output.
- **Instagram:** via Behold JSON feed (Meta's API route was abandoned). Admin Instagram tab → paste feed URL → Connect. Polled every 3 h (free plan: 6 posts, 1,200 reads/month). Falls back to Instagram embeds for posts outside the feed.
- **Dues:** PayPal payment links only; the site never touches card data. Admins press Mark paid; first Mark paid for a website joiner sends the welcome email. Waivers are yearly (Jan–Dec), drawn or typed signature, minors signed by a parent/guardian, Family members' other adults get private `/membership?t=` links.
- **Stats:** counts only by design; no per-member tracking. Weekly visitor codes are salted hashes kept until week end; emails use random per-copy tracking codes not tied to members.
- **Email:** Brevo SMTP from `news@paxvelo.cc` (add DKIM/DMARC in Cloudflare DNS; SMTP login looks like `abc@smtp-brevo.com`). Bulk sends pause after 3 straight failures and resume with **Resume**. Failures: `journalctl -u paxvelo | grep failed`. Replies to `news@` need forwarding (Cloudflare Email Routing).

## Deploy
- Ubuntu 24.04 server. DNS A records → server. `scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'`; then `scp paxvelo.conf root@SERVER:/opt/paxvelo/` and `chown paxvelo` + `chmod 600` it. HTTPS via Caddy (`deploy/Caddyfile`, not copied by deploys: `scp` it to `/etc/caddy/Caddyfile` and `systemctl reload caddy`).
- **CI:** `.github/workflows/publish.yml` runs on pushes to `main`: builds/tests, then uploads and restarts; auto-rolls back if the new version won't start. Needs secrets `DEPLOY_HOST`, `DEPLOY_SSH_KEY`, `DEPLOY_KNOWN_HOSTS` (key authorised for the `deploy` user in `/home/deploy/.ssh/authorized_keys`); variable `SERVER_ARCH=arm64` for ARM. No `DEPLOY_HOST` = check only. Manual: `./deploy/deploy.sh root@SERVER`.
- Currently served at paxvelo.cc while Squarespace keeps paxvelo.club. **Move to .club:** edit first two blocks of `deploy/Caddyfile` and uncomment the redirect block, push the Caddyfile to the server, set `SITE_URL=https://www.paxvelo.club`, restart, repoint DNS, update Strava callback domain, then cancel the Squarespace plan (keep the domain; keep paxvelo.cc for old links). Old Squarespace URLs already redirect.
- Lock SSH to keys only: `scp deploy/lock-ssh.sh root@SERVER:/root/ && ssh root@SERVER 'sh lock-ssh.sh'` (refuses if root has no key; test a second login before closing).

## Backups
- Nightly data-file copy (30 days) and weekly `media-DATE.tgz` of photos+minutes (8 weeks) in `/opt/paxvelo/backups`. Nightly 3:30 am rclone copy to Google Drive (`deploy/paxvelo-offsite`, cron `/etc/cron.d/paxvelo-offsite`, config `/opt/paxvelo/rclone.conf` with `scope = drive.file`; token from `rclone authorize drive --drive-scope drive.file`, treat as secret). Failures log `BACKUP TO GOOGLE DRIVE FAILED` (`journalctl -t paxvelo-offsite`); nothing emails about it. Admin **Home** warns when the newest nightly copy is over two days old (`BACKUP_DIR` in paxvelo.conf if the folder moves) and when the disk is getting full.
- **Restore:** `systemctl stop paxvelo`, copy `paxvelo-DATE.json` to `/opt/paxvelo/data/paxvelo.json`, `chown paxvelo:paxvelo`, `systemctl start paxvelo` (stop first or the running site overwrites it). Unpack the newest `media-*.tgz` into `/opt/paxvelo/data`.
- Admin "Download a full backup" is president-only, text data only (no photos/files, no signing key).

## Squarespace import
Members → Import from a spreadsheet (CSV with email and name columns); skips duplicates, sends no welcome email. From a Squarespace profiles export it reads `Member Areas` as the plan and works out paid years from `Customer Since`, `Last Order Date` and `Order Count` (one payment a year).
