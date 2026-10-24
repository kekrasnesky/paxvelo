# PaxVelo website

> **Want to change the words or photos on the site?** See [content/README.md](content/README.md). Everything is edited right here on GitHub, and the site updates itself.

The whole club site in one small program: the public pages, membership signup with PayPal dues, and an admin portal for managing members and emailing them. It has no database, no plugins, no frameworks, and no third-party code. Everything (pages, styles, fonts, images) is built into a single file you copy to a server. Member data lives in one readable file, `data/paxvelo.json`.

## Try it on your computer

1. Install Go from https://go.dev/dl (version 1.22 or newer).
2. In this folder: `cp paxvelo.conf.example paxvelo.conf`, then set `ADMIN_PASSWORD`. Leave `SMTP_HOST` empty for now so emails are only written to the log.
3. Run `go run .` and open http://localhost:8080. The admin portal is at http://localhost:8080/admin.

After changing any file, stop it (Ctrl+C) and run `go run .` again. `go test ./...` runs the same checks GitHub runs before publishing.

## Where things live

| To change… | Edit |
|---|---|
| Text and photos on a page | `content/<page>.md` and `content/photos/` (guide: [content/README.md](content/README.md)) |
| The Join page | `web/templates/join.html` |
| Dues amounts, PayPal links, kit store, Strava | `paxvelo.conf` (no code changes needed) |
| Header, menu, footer, social links | `web/templates/layout.html` |
| Colors, fonts, spacing | `web/static/site.css` (colors are at the top) |
| The newsletter's layout | `web/templates/newsletter.html` (its content is edited in the admin) |
| Add a page | Create `content/<name>.md` (no code changes needed) |
| Logo and browser-tab icon | `web/static/img/` |

Every `##` heading gets its own address, like `/rides#weekend-rides`. Old Squarespace addresses still work: the two ride pages forward to the matching section of Our Rides, and the old waiver page forwards to the homepage. Short links for flyers and emails: `paxvelo.club/kit` (team store) and `paxvelo.club/strava`.

## Swapping in the real photos

The photos are placeholders cut from a screenshot. Replace them with the originals, keeping the same file names:

- `content/photos/hero.webp`: the big group photo, about 2000px wide (and `hero-phone.webp`, a square crop for phones)
- `content/photos/ride.webp` and `giving.webp`: the two homepage photos, about 1000px wide (cropped to the same shape automatically)
- `web/static/img/logo.png`: the shield at about 280px wide, so it's sharp on phones
- `web/static/img/favicon.png`: the browser-tab icon, 64×64

To make WebP files, drop photos into https://squoosh.app, choose WebP at quality 75, and download. JPGs work too if you change the file name in `content/home.md`.

## The Paceline newsletter

The club's quarterly newsletter lives at `paxvelo.club/newsletter` (always the latest issue), and each issue keeps its own address, like `paxvelo.club/newsletter/2026-fall`. It's written in the admin portal, under **Newsletter**:

1. **Start a new issue** and pick the season. The first issue comes filled in with sample text. Later issues copy the previous one, so regular sections like the new rider corner and the kit call-out carry over.
2. Fill in each section: the featured story (with an optional video link and a quote), last quarter's highlights, upcoming rides with links to their Strava events, a spotlight, three smaller stories, the kit call-out, and a strip of photos. Photos upload right from the form.
3. **Save**, then **Preview** to see exactly how it will look.
4. **Publish** when it's ready. Then **Email members about it** drafts an announcement email on the Email tab.

Text boxes use the same simple formatting as the pages in `content/` (bold, links, `{strava}`, `{kit}`). If something can't be shown, like a mistyped `{placeholder}`, the editor says what to fix and the live issue stays as it was. Published issues are rendered once and served from memory, like every other page.

Uploaded photos are stored on the server in `data/media`. The server backs them up weekly to `/opt/paxvelo/backups`; the **Download a full backup** link covers the text only.

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
4. **Publishing:** connect GitHub (below) so every saved change goes live by itself. To publish by hand instead, run `./deploy/deploy.sh root@SERVER`.

HTTPS certificates are set up and renewed automatically by Caddy, and security updates install themselves. A send that was in progress during a deploy picks up where it left off.

## Automatic publishing

Every change saved to the `main` branch runs [.github/workflows/publish.yml](.github/workflows/publish.yml). It checks that every page builds, then uploads the new version to the server and restarts it. If the check fails, nothing is published and GitHub emails whoever made the change. If the new version doesn't start on the server, the previous one is put back automatically.

One-time setup, after the server setup above:

1. **Make a key for GitHub** on your computer: `ssh-keygen -t ed25519 -N "" -C paxvelo-github -f paxvelo-github`. This makes two files, `paxvelo-github` (private) and `paxvelo-github.pub`.
2. **Let that key publish:** `scp paxvelo-github.pub root@SERVER:/home/deploy/.ssh/authorized_keys`. The `deploy` user that `setup.sh` created can only upload a new version and restart the site.
3. **Give GitHub the details.** In the repository, go to **Settings → Secrets and variables → Actions → New repository secret** and add:
   - `DEPLOY_HOST`: the server's IP address
   - `DEPLOY_SSH_KEY`: the whole contents of the private `paxvelo-github` file
   - `DEPLOY_KNOWN_HOSTS`: the output of `ssh-keyscan SERVER` (this lets GitHub check it's talking to the right server)
4. **Try it:** on the **Actions** tab, open **Publish website** and press **Run workflow**. Then delete the `paxvelo-github` files from your computer: GitHub has the key it needs.

For an ARM server (e.g. Hetzner CAX), also add a variable (same page, **Variables** tab) named `SERVER_ARCH` with the value `arm64`.

Until `DEPLOY_HOST` is set, the workflow still checks every change but doesn't publish anything.

**Recommended:** under **Settings → Collaborators**, give club volunteers the Write role so they can edit pages. Anyone with Write access can publish, so only add people you trust with the website.

## Moving members over from Squarespace

Export your contacts from Squarespace as a CSV, then use **Import from a spreadsheet** on the admin Members page. It finds the email and name columns automatically and skips anyone already on the list. Imported members don't get a welcome email.

## Backups

The server copies the data file every night and keeps 30 days (`/opt/paxvelo/backups`). For a copy off the server, use **Download a full backup** at the bottom of the admin Members page. To restore, put a backup file back as `/opt/paxvelo/data/paxvelo.json` and run `systemctl restart paxvelo`.
