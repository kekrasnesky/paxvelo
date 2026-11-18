# Calendar API, deploy, backups, imports

## Calendar API
```sh
curl "https://www.paxvelo.club/api/rides?from=2026-11-01&to=2026-11-30"   # rides as JSON, leaders included
# Add or update a ride; the id is your own name for it (send again to update).
curl -X PUT https://www.paxvelo.club/api/rides/coffee-2026-11-07 -H "Authorization: Bearer $CALENDAR_API_KEY" \
  -d '{"date":"2026-11-07","time":"08:30","name":"Coffee Ride","place":"Leonardtown Square","groups":["Fast","Social"],"strava_url":"https://www.strava.com/clubs/10197/group_events/123"}'
curl -X DELETE https://www.paxvelo.club/api/rides/coffee-2026-11-07 -H "Authorization: Bearer $CALENDAR_API_KEY"   # or cancelled, if leaders signed up
curl -X POST https://www.paxvelo.club/api/strava/sync -H "Authorization: Bearer $CALENDAR_API_KEY"   # import Strava events now
```
Ride fields: `date` (required), `name` (required), `time` (24-hour), `place`, `details`, `groups` (each gets a leader), `strava_url`, `cancelled`. API changes keep leaders who signed up on the website.

## Deploy
- **Cloudflare CDN** (optional): with the DNS record "Proxied", `deploy/Caddyfile` makes Caddy take the visitor's address from `CF-Connecting-IP` only on connections from Cloudflare's ranges (`trusted_proxies static …`, refresh from cloudflare.com/ips) and pass it as the one `X-Forwarded-For` value `clientIP` reads; without it every visitor would share Cloudflare's addresses (spam limits, waiver IPs). Needs Caddy ≥2.7 from Caddy's apt source: `deploy/cloudflare-prep.sh` upgrades it and installs the Caddyfile (validates first, keeps a backup, rolls back if the site doesn't answer). Cloudflare SSL mode must be Full (strict); its free plan refuses uploads over 100 MB (long 4K clips on Top video).
- Ubuntu 24.04 server. DNS A records → server. `scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'`; then `scp paxvelo.conf root@SERVER:/opt/paxvelo/` and `chown paxvelo` + `chmod 600` it. HTTPS via Caddy (`deploy/Caddyfile`, not copied by deploys: `scp` it to `/etc/caddy/Caddyfile` and `systemctl reload caddy`).
- **CI:** `.github/workflows/publish.yml` runs on pushes to `main`: builds/tests, then uploads and restarts; auto-rolls back if the new version won't start. Needs secrets `DEPLOY_HOST`, `DEPLOY_SSH_KEY`, `DEPLOY_KNOWN_HOSTS` (key authorised for the `deploy` user in `/home/deploy/.ssh/authorized_keys`); variable `SERVER_ARCH=arm64` for ARM. No `DEPLOY_HOST` = check only. Manual: `./deploy/deploy.sh root@SERVER`.
- Served at www.paxvelo.club since Nov 12 2026 (DNS on Cloudflare; Google Workspace MX records kept; registration still at Squarespace until transferred); `deploy/Caddyfile` redirects the old paxvelo.cc addresses to it, so keep paxvelo.cc registered for old links.
- Lock SSH to keys only: `scp deploy/lock-ssh.sh root@SERVER:/root/ && ssh root@SERVER 'sh lock-ssh.sh'` (refuses if root has no key; test a second login before closing).

## Backups
- Nightly data-file copy (30 days) and weekly `media-DATE.tgz` of photos+minutes+receipts (8 weeks; the cron line is written by `deploy/setup.sh`, so an existing server needs `receipts` added to it by hand) in `/opt/paxvelo/backups`. Nightly 3:30 am rclone copy to Google Drive (`deploy/paxvelo-offsite`, cron `/etc/cron.d/paxvelo-offsite`, config `/opt/paxvelo/rclone.conf` with `scope = drive.file`; token from `rclone authorize drive --drive-scope drive.file`, treat as secret). Failures log `BACKUP TO GOOGLE DRIVE FAILED` (`journalctl -t paxvelo-offsite`); nothing emails about it. Admin **Home** warns when the newest nightly copy is over two days old (`BACKUP_DIR` in paxvelo.conf if the folder moves) and when the disk is getting full.
- **Restore:** `systemctl stop paxvelo`, copy `paxvelo-DATE.json` to `/opt/paxvelo/data/paxvelo.json`, `chown paxvelo:paxvelo`, `systemctl start paxvelo` (stop first or the running site overwrites it). Unpack the newest `media-*.tgz` into `/opt/paxvelo/data`.
- Admin "Download a full backup" is president-only, text data only (no photos/files, no signing key).

## Squarespace import
Members → Import from a spreadsheet (CSV with email and name columns); skips duplicates, sends no welcome email. From a Squarespace profiles export it reads `Member Areas` as the plan and works out paid years from `Customer Since`, `Last Order Date` and `Order Count` (one payment a year). A Squarespace **orders** export (`orders.go`, spotted by its `Paid at` + `Lineitem name` columns) instead sets each payer's dues to end a year after their latest membership payment (join day moves to the payment day, first-join year kept; refunded/cancelled/non-membership orders ignored) and adds payers not on the list; the flash names who was added (IDs in the URL, never emails).
