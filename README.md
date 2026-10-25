# PaxVelo website

> **Want to change the words or photos on the site?** The easiest way is the admin portal (`paxvelo.club/admin`): the **Pages** tab edits any page, and the **Board** tab edits the board members. Pages can also be edited here on GitHub: see [content/README.md](content/README.md).

The whole club site in one small program: the public pages, membership signup with PayPal dues, and an admin portal for managing members and emailing them. It has no database, no plugins, no frameworks, and no third-party code. Everything (pages, styles, fonts, images) is built into a single file you copy to a server. Member data lives in one readable file, `data/paxvelo.json`.

## Try it on your computer

1. Install Go from https://go.dev/dl (version 1.22 or newer).
2. In this folder: `cp paxvelo.conf.example paxvelo.conf`, then set `ADMIN_PASSWORD` (at least 12 characters: it's the setup password for the first login). Leave `SMTP_HOST` empty for now so emails are only written to the log.
3. Run `go run .` and open http://localhost:8080. The admin portal is at http://localhost:8080/admin.

After changing any file, stop it (Ctrl+C) and run `go run .` again. `go test ./...` runs the same checks GitHub runs before publishing.

## Where things live

| To change… | Edit |
|---|---|
| Text and photos on a page | Admin portal → **Pages**, or `content/<page>.md` on GitHub (guide: [content/README.md](content/README.md)) |
| Board members and their photos | Admin portal → **Board** |
| The Join page | `web/templates/join.html` |
| Dues amounts, PayPal links, kit store, Strava | `paxvelo.conf` (no code changes needed) |
| Header, menu, footer, social links | `web/templates/layout.html` |
| Colors, fonts, spacing | `web/static/site.css` (colors are at the top) |
| The newsletter's layout | `web/templates/newsletter.html` (its content is edited in the admin) |
| Add a page | Create `content/<name>.md` (no code changes needed) |
| Logo and browser-tab icon | `web/static/img/` (see "Swapping in the real photos") |

Every `##` heading gets its own address, like `/rides#weekend-rides`. Old Squarespace addresses still work: the two ride pages forward to the matching section of Our Rides, and the old waiver page forwards to the homepage. Short links for flyers and emails: `paxvelo.club/kit` (team store) and `paxvelo.club/strava`.

## Swapping in the real photos

The photos are placeholders cut from a screenshot. Replace them with the originals, keeping the same file names:

- `content/photos/hero.webp`: the big group photo, about 2000px wide (and `hero-phone.webp`, a square crop for phones)
- `content/photos/ride.webp` and `giving.webp`: the two homepage photos, about 1000px wide (cropped to the same shape automatically)
- The club logo is `web/static/img/logo.svg`, also used as the browser-tab icon. `logo-light.svg` is the same logo with the black parts in cream, for the dark header. `logo.png` (for emails) and `og.jpg` (the picture shown when someone shares a link) are made from it.

To make WebP files, drop photos into https://squoosh.app, choose WebP at quality 75, and download. JPGs work too if you change the file name in `content/home.md`.

## The newsletter

The club's monthly newsletter lives at `paxvelo.club/newsletter` (always the latest issue), and each issue keeps its own address, like `paxvelo.club/newsletter/2026-10`. It's written in the admin portal, under **Newsletter**:

1. **Start a new draft** and pick the month. The first issue comes with sample blocks to replace. Later drafts start with the same blocks as the latest published issue, keeping each regular feature's heading and link (like "New Rider Corner") but not last month's text and photos. Banners are one-offs, so they don't carry over.
2. Build the issue from blocks: **Add a block** at the bottom, and move or remove blocks with the buttons on each. The kinds are:
   - **Big story:** text beside a large photo or video, for the lead story.
   - **Story:** a column with a photo. Stories next to each other share a row, up to three across.
   - **Rides from the calendar:** filled in automatically with every ride in the 5 weeks after the issue is published (cancelled rides are left out), with each pace group's leader. Rides that still need a leader get a **Lead this ride** button to their calendar page, and a line at the top says how many need leaders. It stays live: when someone signs up on the calendar, the newsletter shows it.
   - **Banner:** a dark band with a button, for one-off asks like a kit order or a fundraiser.
   - **Photo strip** (up to 8 photos) and **Instagram posts** (up to 3).
3. **Save and preview** to see exactly how it will look. The page is laid out like a printed newsletter: a "The PaxVelo Newsletter" nameplate between rules, serif type, the cover photo framed below it, a drop cap on the big story, stories divided by thin rules, the rides as a boxed ride schedule, and banners as boxed notices. Any photo (and the cover) can have a short caption. "In this issue" at the top lists the stories with headings, linking down the page (it shows once there are at least two). Printing the page gives just the newsletter, without the website's menu.
4. **Save and publish** when it's ready. Then **Email members about it** drafts an announcement email on the Email tab, listing the rides that still need leaders with a link to each.

A month can have several drafts, for example two people trying different versions, or **Make another draft from this** to try a variation. Only one issue per month can be published: the others can't be published until it's unpublished. The list shows which admin started each draft and who edited it last.

Text boxes use the same simple formatting as the pages in `content/` (bold, links, `{strava}`, `{kit}`). If something can't be shown, like a mistyped `{placeholder}`, the editor says what to fix and the live issue stays as it was. Published issues are rendered once and served from memory, like every other page.

Photos upload the moment you pick them or drop them on a photo box, so you see them right away; **Remove photo** takes one out straight away too. Every photo has framing: click the spot to keep in view (like faces), and the small previews show how it's cropped. The photo strip takes several photos at once (up to 8): each shows the crop the strip uses, the arrows change the order, and **Remove** takes one out.

Uploaded photos are shrunk automatically so pages load fast, even on a phone: JPG and PNG photos are made at most 2000 pixels on their long side and saved as JPG, usually under 400 KB (a PNG with see-through parts stays a PNG). Sideways phone photos are turned upright, and the camera's hidden details, like where the photo was taken, are dropped. WebP and GIF photos are kept as they are. This applies everywhere photos are uploaded: newsletters, pages, and the board. Photos can be up to 8 MB.

Uploaded photos are stored on the server in `data/media`. The server backs them up weekly (with the minutes files) to `/opt/paxvelo/backups`; the **Download a full backup** link covers the text only.

## Admin accounts

Each admin logs in at `paxvelo.club/admin` with their own email address and password.

- **The first login:** before any accounts exist, log in with your name and the setup password (`ADMIN_PASSWORD` in `paxvelo.conf`, at least 12 characters). Then open the **Admins** tab and add yourself and the other admins.
- **Adding an admin:** enter their name and email. They get an email with a link to choose their password (it works for 7 days). **Send a new link** next to their name sends another.
- **Forgotten passwords:** **Forgot your password?** on the login page emails a link that works for an hour. It says the same thing whether or not the email belongs to an admin.
- **Removing an admin** logs them out everywhere. So does choosing a new password (**Change your password** on the Admins tab).
- **Emergency key:** `ADMIN_PASSWORD` still logs in with any admin's email, for whoever runs the server, in case email stops working. Changing it logs everyone out.

Passwords are stored as PBKDF2 hashes, never as the password itself. In test mode (no `SMTP_HOST`), the Admins tab shows each password link instead of emailing it.

## Editing pages and the board in the admin

- **Pages** lists every page. Open one to change its text, using the same simple formatting described in [content/README.md](content/README.md). **Add a photo** uploads a photo and puts it where your cursor is. A page that can't be shown (like a missing title) isn't saved, so the live page never breaks.
- **Add a page** (at the bottom of the Pages tab) makes a new page from just a title: the address is filled in from it (like `/group-ride-etiquette`), and it can go straight into the Info menu, at the end. It starts with sample text showing the formatting and opens to edit. It's live as soon as it's added. Pages added here can be deleted again; pages from GitHub can only go back to their GitHub version.
- An edit made in the admin wins over the GitHub version of that page. **Go back to the GitHub version** undoes the admin edits. The Pages list shows which pages were edited where.
- **Board** lists the board members: name, role, photo, and their position on the page. Click a photo to keep the person's face in view; board photos show as circles.

## Ride calendar

`paxvelo.club/calendar` shows every club ride by month, and anyone can look at it.

- **Weekly rides** (the Tuesday and Thursday rides to start with) are set up in the admin portal under **Rides**, with a day, a start time, an optional season, and **pace groups**. Press **Edit** on a weekly ride to change it: its dates from today on follow, except anything changed on one date's own page. The Thursday ride runs March 1 to October 10. Both are set to 6:00 PM for now: check the times and fix them on the Rides tab if they're wrong.
- **Pace groups:** pick from a menu: one leader, A and B groups, or A, B and C groups. Each group has its own leader. Put the paces in the ride's details (like "A 24–28 mph, B 20–22 mph"). The calendar shows green when every group has a leader, blue when some do, and amber when none do.
- **Leading a ride:** no account or sign-in needed. On the ride's page, type your name and press **I'll lead**. A spot with no leader can be taken by anyone. Once it has a leader, only an admin can change it (in the admin portal, or a Strava club admin signed in on the calendar).
- **Adding a ride:** **Add a ride** (or the + on any day) needs just your name, a date, a start time, and a ride name. Everything else is optional.
- **In the admin portal:** the **Calendar** tab shows every month; click any ride to change its leaders (including filled spots), name, time, start, details, pace groups, or Strava link, or to cancel it. One-off rides can also be moved to another date or deleted. **Add a ride** (or the + on a day) adds one with its leaders.
- **Emails:** every new leader, change of leader, step-down, new ride, and cancellation is emailed to PaxVeloClub@gmail.com (or `RIDES_EMAIL`), with the current leaders of each group.
- **Spam:** the sign-up forms use the same checks as the Join form, so bots can't fill the calendar.

**About Strava RSVPs:** Strava only lets a person RSVP to an event themselves, from their own Strava account, so the website can't RSVP someone for an event by name. Leaders who sign up with just a name show on the calendar and in the email; the club's Strava event is linked from every ride's page.

### Importing the club's Strava events

The club's upcoming Strava events can show up on the calendar automatically:

1. Set up the Strava API application (below).
2. On the admin's **Rides** tab, press **Connect Strava** and approve with a Strava account that's in the club (a club admin is best).

From then on, the site reads the club's events every hour (and whenever you press **Import now**). An event that matches a weekly ride (same day, and the same name or start time) is linked to that weekly ride instead of being added twice. Leaders who signed up on the website are kept when an event changes. An event deleted on Strava leaves the calendar, unless leaders had signed up for it, in which case it's marked cancelled.

Strava's public API documents clubs but not club events, so this reads them the way Strava's own apps do. If Strava ever changes that, the Rides tab shows the error and the calendar carries on without the import.

**Setting up the Strava API application** (once, by a club admin):

1. Signed in to Strava, go to https://www.strava.com/settings/api and create an application: name *PaxVelo Cycling Club*, category *Social*, website `https://www.paxvelo.cc`, and **Authorization Callback Domain** `www.paxvelo.cc` (change both to paxvelo.club when the site moves there).
2. Copy its **Client ID** and **Client Secret** into `paxvelo.conf` as `STRAVA_CLIENT_ID` and `STRAVA_CLIENT_SECRET`, then restart the site.

New Strava applications allow one connected athlete, which is all the import needs. Club admins can also **Sign in with Strava** on the calendar to change leaders there; for more than one of them, use **Request higher capacity** on the same Strava page.

### Calendar API

Scripts and other tools can read and change the calendar. Set `CALENDAR_API_KEY` in `paxvelo.conf` to a long random password (for example the output of `openssl rand -hex 24`) to turn on changes; reading needs no key.

```sh
# Every ride in November, as JSON (leaders included)
curl "https://www.paxvelo.club/api/rides?from=2026-11-01&to=2026-11-30"

# Add or update a ride. "coffee-2026-11-07" is your own name for it:
# send it again with changes to update the same ride.
curl -X PUT https://www.paxvelo.club/api/rides/coffee-2026-11-07 \
  -H "Authorization: Bearer $CALENDAR_API_KEY" \
  -d '{"date":"2026-11-07","time":"08:30","name":"Coffee Ride","place":"Leonardtown Square","groups":["Fast","Social"],"strava_url":"https://www.strava.com/clubs/10197/group_events/123"}'

# Remove it (or mark it cancelled, if leaders had signed up)
curl -X DELETE https://www.paxvelo.club/api/rides/coffee-2026-11-07 -H "Authorization: Bearer $CALENDAR_API_KEY"

# Import the club's Strava events right now
curl -X POST https://www.paxvelo.club/api/strava/sync -H "Authorization: Bearer $CALENDAR_API_KEY"
```

Ride fields: `date` (required, `2026-11-07`), `name` (required), `time` (24-hour, `08:30`), `place`, `details`, `groups` (pace groups, each gets a leader), `strava_url`, and `cancelled` (true or false). Changes made through the API keep any leaders who signed up on the website.

## Instagram

The home page has an "On Instagram" section. Pick which posts it shows in the admin portal under **Instagram**: paste up to 6 post links (on Instagram: ⋯ → Copy link). Each newsletter issue can also show up to 3 posts. Posts are shown with Instagram's own embed, so photos, carousels, and reels look as they do on Instagram, and they link back to the club's page. That embed loads a script from Instagram, which Instagram may use to track visitors who are logged in.

Showing the newest posts automatically would mean switching the account to a Business account linked to a Facebook Page and setting up a Meta developer app, with a key to renew every 60 days. Picking posts by hand needs none of that.

## Meeting minutes

Upload minutes in the admin portal under **Minutes**: the meeting date, a title, an optional one-line summary, and the file (PDF or Word, up to 20 MB). They stay hidden until you press **Publish**, then appear on the public **Meeting Minutes** page (`paxvelo.club/minutes`, also in the Info menu), newest first and grouped by year. PDFs open in the browser; Word files download.

Files are stored on the server in `data/files`. Like photos, they're not part of the **Download a full backup** file, so keep the originals.

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

You need a small Ubuntu 24.04 server (about $5/month at RackNerd, Vultr, or similar) and access to the domain's DNS settings.

For now the site runs at **paxvelo.cc**, while the old Squarespace site keeps **paxvelo.club**. The two don't affect each other. The address is set in just two places: `deploy/Caddyfile` (HTTPS) and `SITE_URL` in `paxvelo.conf` (links in emails). When it's time, see "Moving to paxvelo.club" below.

1. **DNS:** point both `paxvelo.cc` and `www.paxvelo.cc` (A records) at the server's IP address. Leave the paxvelo.club DNS alone.
2. **One-time server setup:** `scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'`
3. **Settings:** `scp paxvelo.conf root@SERVER:/opt/paxvelo/` then `ssh root@SERVER 'chown paxvelo /opt/paxvelo/paxvelo.conf && chmod 600 /opt/paxvelo/paxvelo.conf'`
4. **Publishing:** connect GitHub (below) so every saved change goes live by itself. To publish by hand instead, run `./deploy/deploy.sh root@SERVER`.

HTTPS certificates are set up and renewed automatically by Caddy, and security updates install themselves. A send that was in progress during a deploy picks up where it left off.

### Moving to paxvelo.club

When the new site is ready to replace Squarespace:

1. **Caddyfile:** in `deploy/Caddyfile`, change `paxvelo.cc` to `paxvelo.club` in the first two blocks, and remove the `#` in front of each line of the last block, so old paxvelo.cc links (in emails already sent, say) go to the same page on paxvelo.club. Save it to GitHub, then put it on the server: `scp deploy/Caddyfile root@SERVER:/etc/caddy/Caddyfile && ssh root@SERVER 'systemctl reload caddy'`. (Publishing doesn't copy the Caddyfile, only the site.)
2. **Settings:** on the server, set `SITE_URL=https://www.paxvelo.club` in `/opt/paxvelo/paxvelo.conf`, then `ssh root@SERVER 'systemctl restart paxvelo'`.
3. **DNS:** in the paxvelo.club DNS settings, replace Squarespace's records for `paxvelo.club` and `www` with A records pointing at the server's IP address. Keep the paxvelo.cc records too, so the redirect works. Caddy gets the new HTTPS certificate on its own within a few minutes of the DNS change.
4. **Strava:** if the Strava app is set up, change its "Authorization Callback Domain" to `www.paxvelo.club`.
5. **Squarespace:** once paxvelo.club shows the new site, cancel the Squarespace website plan, but keep the paxvelo.club domain registration (it can stay at Squarespace or move to another registrar). Keep paxvelo.cc renewing too while old links are still around.

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
