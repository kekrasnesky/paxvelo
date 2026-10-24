# PaxVelo website

The whole club site in one small program: the public pages, membership signup with PayPal dues, and an admin portal for managing members and emailing them. It has no database, no plugins, no frameworks, and no third-party code. Everything (pages, styles, fonts, images) is built into a single file you copy to a server. Member data lives in one readable file, `data/paxvelo.json`.

## Try it on your computer

1. Install Go from https://go.dev/dl (version 1.22 or newer).
2. In this folder: `cp paxvelo.conf.example paxvelo.conf`, then set `ADMIN_PASSWORD`. Leave `SMTP_HOST` empty for now so emails are only written to the log.
3. Run `go run .` and open http://localhost:8080. The admin portal is at http://localhost:8080/admin.

After changing any file, stop it (Ctrl+C) and run `go run .` again.

## Where things live

| To change… | Edit |
|---|---|
| Text on a page | `web/templates/<page>.html` (home, rides, bike-safety, committee, join) |
| Dues amounts, PayPal links, kit store, Strava | `paxvelo.conf` (no code changes needed) |
| Header, menu, footer, social links | `web/templates/layout.html` |
| Colors, fonts, spacing | `web/static/site.css` (colors are at the top) |
| Add a page | Create a template, then add one line to `pageList` (and `infoLinks` for the Info menu) in `site.go` |
| Photos and logo | `web/static/img/` (see below) |

Old Squarespace addresses still work: the two ride pages forward to the matching section of Our Rides, and the old waiver page forwards to the homepage. Short links for flyers and emails: `paxvelo.club/kit` (team store) and `paxvelo.club/strava`.

## Swapping in the real photos

The images in `web/static/img/` are placeholders cut from a screenshot. Replace them with the originals, keeping the same file names:

- `hero.webp`: the big group photo, about 2000px wide
- `ride.webp` and `giving.webp`: the two homepage photos, about 1000px wide (cropped to the same shape automatically)
- `logo.png`: the shield at about 280px wide, so it's sharp on phones
- `favicon.png`: the browser-tab icon, 64×64

To make WebP files, drop photos into https://squoosh.app, choose WebP at quality 75, and download. JPGs work too if you change the file name in `web/templates/home.html`.

## Membership dues

Dues are $35 (Individual) or $45 (Family) per calendar year, paid through PayPal. To set it up:

1. Create a free PayPal Business account for the club, if you don't have one.
2. In PayPal, go to **Pay Links and Buttons** and create two payment links: Individual for $35 and Family for $45.
3. Paste them into `paxvelo.conf` as `PAYPAL_INDIVIDUAL_URL` and `PAYPAL_FAMILY_URL`.

When someone joins, they pick a plan and land on the right PayPal link. PayPal emails the club for each payment. Find that person on the admin Members page and press **Mark paid**. The **Not paid** filter shows who still owes. On January 1 everyone shows as not paid for the new year, so you can email just that group a reminder. Put `{pay_link}` in the message and each member gets their own plan's link.

The site never handles card details or connects to PayPal directly, so there's nothing to maintain. To change the prices, update `DUES_INDIVIDUAL` and `DUES_FAMILY` and the PayPal links. For a separate donation option, add a PayPal donation link as `DONATE_URL` and a "Support the club" link appears on the homepage.

## Email setup (club Gmail)

1. Turn on 2-Step Verification for PaxVeloClub@gmail.com.
2. Create an app password at https://myaccount.google.com/apppasswords.
3. In `paxvelo.conf`, set `SMTP_PASS` to that password (no spaces) and keep `SMTP_HOST=smtp.gmail.com`.
4. In the admin, Email → **Send test** to yourself before sending to everyone.

Gmail allows about 500 emails a day. If the club outgrows that, any email service that offers SMTP (Amazon SES, Postmark, Fastmail…) works by changing the `SMTP_` lines. No code changes are needed.

Every email has an unsubscribe link, and Gmail and Yahoo show their own one-click "Unsubscribe" button for it. If sending fails three times in a row (wrong password, Gmail down), it pauses with the reason shown. Nobody is skipped: fix the problem and press **Resume**.

## Putting it online

You need a small Ubuntu 24.04 server (about $5/month at Hetzner, DigitalOcean, or similar) and access to the paxvelo.club DNS settings.

1. **DNS:** point both `paxvelo.club` and `www.paxvelo.club` (A records) at the server's IP address.
2. **One-time server setup:** `scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'`
3. **Settings:** `scp paxvelo.conf root@SERVER:/opt/paxvelo/` then `ssh root@SERVER 'chown paxvelo /opt/paxvelo/paxvelo.conf && chmod 600 /opt/paxvelo/paxvelo.conf'`
4. **Deploy:** `./deploy/deploy.sh root@SERVER`. Run this again any time you change something.

HTTPS certificates are set up and renewed automatically by Caddy, and security updates install themselves. A send that was in progress during a deploy picks up where it left off.

## Moving members over from Squarespace

Export your contacts from Squarespace as a CSV, then use **Import from a spreadsheet** on the admin Members page. It finds the email and name columns automatically and skips anyone already on the list. Imported members don't get a welcome email.

## Backups

The server copies the data file every night and keeps 30 days (`/opt/paxvelo/backups`). For a copy off the server, use **Download a full backup** at the bottom of the admin Members page. To restore, put a backup file back as `/opt/paxvelo/data/paxvelo.json` and run `systemctl restart paxvelo`.
