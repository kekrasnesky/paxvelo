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

Every `##` heading gets its own address, like `/rides#weekend-rides`. Once paxvelo.club points at this server (see "Moving to paxvelo.club"), old Squarespace addresses still work: the two ride pages forward to the matching section of Our Rides, and the old waiver page forwards to the membership page, where the waiver is signed now. Short links for flyers and emails: `paxvelo.club/kit` (team store) and `paxvelo.club/strava`.

## Swapping in the real photos

The photos are placeholders cut from a screenshot. Replace them with the originals, keeping the same file names:

- `content/photos/hero.webp`: the big group photo, about 2000px wide (and `hero-phone.webp`, a square crop for phones)
- `content/photos/ride.webp` and `giving.webp`: the two homepage photos, about 1000px wide (cropped to the same shape automatically)
- The club logo is `web/static/img/logo.svg`. The browser-tab icon, `favicon.svg`, is just its P and V, without the stripes, so they're big enough to make out. `logo-light.svg` is the same logo with the black parts in cream, for the dark header. `logo.png` (for emails) and `og.jpg` (the picture shown when someone shares a link) are made from it.

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
   - **Share your photos:** a box asking readers to send in their ride photos ("Want to see your photos in the newsletter or gallery?"), with a button to the share page (below). Its heading, text and button words can be changed. New drafts carry it over with its wording, and put it under the photo strip if the last issue didn't have one.
3. **Save and preview** to see exactly how it will look. The page is laid out like a printed newsletter: a "The PaxVelo Newsletter" nameplate between rules, serif type, the cover photo framed below it, a drop cap on the big story, stories divided by thin rules, the rides as a boxed ride schedule, and banners as boxed notices. Any photo (and the cover) can have a short caption. "In this issue" at the top lists the stories with headings, linking down the page (it shows once there are at least two). Printing the page gives just the newsletter, without the website's menu.
4. **Save and publish** when it's ready. Then **Email members about it** drafts an announcement email on the Email tab, listing the rides that still need leaders with a link to each.

A month can have several drafts, for example two people trying different versions, or **Make another draft from this** to try a variation. Only one issue per month can be published: the others can't be published until it's unpublished. The list shows which admin started each draft and who edited it last.

Text boxes use the same simple formatting as the pages in `content/` (bold, links, `{strava}`, `{kit}`). If something can't be shown, like a mistyped `{placeholder}`, the editor says what to fix and the live issue stays as it was. Published issues are rendered once and served from memory, like every other page.

Photos upload the moment you pick them or drop them on a photo box, so you see them right away; **Remove photo** takes one out straight away too. Every photo has framing: click the spot to keep in view (like faces), and the small previews show how it's cropped. The photo strip takes several photos at once (up to 8): each shows the crop the strip uses, the arrows change the order, and **Remove** takes one out.

Uploaded photos are shrunk automatically so pages load fast, even on a phone: JPG and PNG photos are made at most 2000 pixels on their long side and saved as JPG, usually under 400 KB (a PNG with see-through parts stays a PNG). Sideways phone photos are turned upright, and the camera's hidden details, like where the photo was taken, are dropped. WebP and GIF photos are kept as they are. This applies everywhere photos are uploaded: newsletters, pages, and the board. Photos can be up to 8 MB.

**Photos from members.** Anyone can send in photos at `/share-photos`, with no account or app: they tap the big **Add photos** box to pick photos from their phone (or drag them onto it on a computer), up to 30 at a time, and each shows as a tile with a × to take it out and an optional caption. They type their name (shown as the photo credit), can pick which ride or event it was from the latest albums or name a new one ("Something else…"), add an email or a note, and tick a box saying they can share them and the club can use them in the newsletter, website and social media. The page shrinks each photo on the phone before it uploads (to 2000 pixels, so a 5 MB photo becomes about half a megabyte and sends quickly on a phone signal), then sends them with a progress bar, filling each tile and ticking it once it's up. The server checks and shrinks each one again like any upload, dropping where it was taken. The page has the join form's spam checks (a hidden box and a form token), allows 5 sends an hour per connection, and stops taking photos while 300 are waiting. Sent-in photos are kept privately in the data folder's `submitted` folder, listed in the data file (`submitted_photos`); only admins can see them, on **Email → Sent-in photos**, the review queue. Each person's photos show together with their note and captions, a **Tick all** box, and the album to approve them into (the one they picked, or a new album by the name they typed, which an admin can change). **Approve ticked** puts the ticked photos in the gallery; **Reject ticked** deletes them for good (a copy already in a newsletter or the gallery stays there). Ticked photos can also go into an issue's first photo strip with **Add to photo strip** (adding one, with the Share your photos box under it, if it has none), up to its 8 photos; then open the issue to frame them. Once all of someone's photos are approved or used, they fold away under **Already reviewed**. **Club PIN:** on **Website → Gallery** an admin can set a PIN (4 to 40 characters; off when empty) to share with members. While it's set, the share page has a **Club PIN** box: with the right PIN (any case), photos skip the review and go straight into the album the member picked, or a new album they named; with no album picked they still wait for review. The same PIN lets members add rides to the calendar without waiting for approval. A wrong PIN is refused, and each connection gets 10 PIN tries an hour, right or wrong, across photos and rides. Photos posted this way still show on Sent-in photos, marked **Posted with the PIN**, and can be taken out of their album. If the PIN gets around, change it or turn it off. Each photo can be downloaded. **Needs attention** says when new photos come in, until an admin opens the screen. The announcement email for an issue with the Share your photos block links to the page too.

**Photo gallery.** `/gallery` (Photo Gallery in the Info menu) is a grid of albums, one for each ride or event, each with its cover photo, date and photo count, in the order admins set. An album (`/gallery/12-halloween-ride`; only the number counts, so renaming keeps links working) shows its photos as a square grid, 3 across on a phone, 48 at a time with **Load more photos**. Tapping a photo opens it full screen with its caption and "Photo by …", arrows, arrow keys or a swipe to step through (a swipe down closes it), **Share** (the phone's share sheet, or copies the link elsewhere) and **Download**. While it's open the address bar shows the photo's own page (`/gallery/12-halloween-ride/345`), which also works on its own, with the photo as the preview image when the link is posted. Each gallery photo is kept at its full size (at most 1600 pixels) plus 960 and 480 pixel copies in the media folder; pages lazy-load the small ones and let the browser pick a size. None of them keep where the photo was taken (JPG, PNG and WebP hidden details are dropped). **Website → Gallery** lists the albums: start one (a name and an optional date), move them up or down, and open one to rename it, delete it (moving its photos to another album, or deleting them too), or upload up to 200 photos straight into it by one photographer (the page shrinks each one and sends them 10 at a time; uploads stop while the server has under 2 GB free). In an album, type captions, drag photos into order or sort them oldest first, newest first or by photographer, and with the ticked photos: **Make cover**, **Move to start** or **end**, **Move to album**, **Set credit**, or **Take out**, which deletes their files (a sent-in photo can be approved again if it's still there). An album shows on the website once it has photos. Photos added before albums were put into albums by ride and date ("TNR · Oct 28, 2026", undated ones in "Older photos") the first time the site started with albums, and their smaller copies are made in the background. The data file lists albums under `albums` and photos under `gallery`, in album order.

Uploaded photos are stored on the server in `data/media`. The server backs them up weekly (with the minutes files) to `/opt/paxvelo/backups`; the **Download a full backup** link covers the text only.

## Admin accounts

Each admin logs in at `paxvelo.club/admin` with their own email address and password.

- **The first login:** before any accounts exist, log in with your name and the setup password (`ADMIN_PASSWORD` in `paxvelo.conf`, at least 12 characters). Then open the **Admins** tab and add yourself and the other admins.
- **Adding an admin:** enter their name and email. They get an email with a link to choose their password (it works for 7 days). **Send a new link** next to their name sends another.
- **Forgotten passwords:** **Forgot your password?** on the login page emails a link that works for an hour. It says the same thing whether or not the email belongs to an admin.
- **The president:** one admin is marked **President** on the Admins tab, and only they can remove admins or delete members. Every other admin can do everything else, including adding admins and members and marking dues paid. The president hands the role on with **Make president** next to someone else. Until anyone is president, any admin can name the first one. The setup password counts as president only until the first admin has chosen a password. **Download a full backup** (Members tab) is president-only too, and leaves out the site's signing key.
- **Last login** on the Admins tab shows when each admin last logged in. Admins stay logged in for 30 days on the same device, so it's the last time they typed their password, not necessarily the last time they used the admin.
- **Removing an admin** logs them out everywhere. So does choosing a new password (**Change your password** on the Admins tab).
- **The setup password** stops logging people in by name once any admin has chosen a password, and logins already open that way end at that point. Add yourself first, choose your password from the emailed link, and use your email to log in from then on.
- **Emergency key:** `ADMIN_PASSWORD` still logs in with any admin's email, for whoever runs the server, in case email stops working. Every use is written to the server log (`admin login with the setup password as …`), so look there if it ever shows up when you haven't used it. Changing it logs everyone out.

Passwords are stored as PBKDF2 hashes, never as the password itself. In test mode (no `SMTP_HOST`), the Admins tab shows each password link instead of emailing it.

## Editing pages and the board in the admin

- **Pages** lists every page. Open one to change its text, using the same simple formatting described in [content/README.md](content/README.md). **Add a photo** uploads a photo and puts it where your cursor is. A page that can't be shown (like a missing title) isn't saved, so the live page never breaks.
- **Add a page** (at the bottom of the Pages tab) makes a new page from just a title: the address is filled in from it (like `/group-ride-etiquette`), and it can go straight into the Info menu, at the end. It starts with sample text showing the formatting and opens to edit. It's live as soon as it's added. Pages added here can be deleted again; pages from GitHub can only go back to their GitHub version.
- An edit made in the admin wins over the GitHub version of that page. **Go back to the GitHub version** undoes the admin edits. The Pages list shows which pages were edited where.
- **Board** lists the board members: name, role, photo, and their position on the page. Click a photo to keep the person's face in view; board photos show as circles.

## Ride calendar

`paxvelo.club/calendar` shows every club ride by month, and anyone can look at it.

- **Weekly rides** are set up in the admin portal under **Rides**, with a day, a start time, an optional season, and **pace groups**. A new site starts with none, so add the club's regular rides there first. A ride whose time changes partway through the year (like the Tuesday Night Ride, 5:30 PM until mid-September, then 5:00 PM) is two weekly rides with back-to-back seasons. Press **Edit** on a weekly ride to change it: its dates from today on follow, except anything changed on one date's own page.
- **Pace groups:** pick from a menu: one leader, A and B groups, or A, B and C groups. Each group has its own leader. Put the paces in the ride's details (like "A 24–28 mph, B 20–22 mph"). The calendar shows green when every group has a leader, blue when some do, and amber when none do.
- **Levels:** every ride gets a colored tag so new riders know how fast it goes: **Social** (16–18 mph), **Intermediate** (18–20 mph), or **Elite** (20+ mph), with a key above the upcoming rides. The club's regular rides get theirs by name: the Tuesday and Thursday Night Rides (TNR and ThNR) are Intermediate or Elite depending on who turns out, and SaMR, SuMR and the Old Line shop rides are Social. Other Strava rides go by the event's pace (Casual is Social, Tempo is Intermediate, Hammerfest is Elite), and anything else is Intermediate. An admin can pick a ride's level instead with the **Level** menu on a weekly ride or on one date's page.
- **Leading a ride:** no account or sign-in needed. On the ride's page, type your name and press **I'll lead**. A spot with no leader can be taken by anyone. Once it has a leader, only an admin can change it (in the admin portal, or a Strava club admin signed in on the calendar).
- **Adding a ride:** **Add a ride** (or the + on any day) needs just your name, a date, a start time, and a ride name. Everything else is optional. A ride added by someone who isn't signed in with Strava waits for an admin to approve it: it isn't on the calendar, the newsletter, or the API until then. The club is emailed, and the admin Calendar tab lists these under **Waiting for approval**, with **Approve**, **Open**, and **Delete**. A rider signed in with Strava (so in the club) skips the wait, and so does anyone who types the club PIN (see **Club PIN** under Photos from members): while a PIN is set, the form has a **Club PIN** box, and a wrong PIN is refused.
- **In the admin portal:** the **Calendar** tab shows every month; click any ride to change its leaders (including filled spots), name, time, start, details, pace groups, or Strava link, or to cancel it. One-off rides can also be moved to another date or deleted. **Add a ride** (or the + on a day) adds one with its leaders.
- **Emails:** every new leader, change of leader, step-down, new ride, and cancellation is emailed to PaxVeloClub@gmail.com (or `RIDES_EMAIL`), with the current leaders of each group.
- **Spam:** the sign-up forms use the same checks as the Join form, so bots can't fill the calendar, and the approval step above stops anyone from posting made-up rides.
- **Strava club admins:** signing in with Strava as a club admin gives admin powers on the calendar for a day. After that the rider stays signed in but has to sign in again to be an admin, so someone removed as a club admin on Strava loses the powers the next day.

**About Strava RSVPs:** Strava only lets a person RSVP to an event themselves, from their own Strava account, so the website can't RSVP someone for an event by name. Leaders who sign up with just a name show on the calendar and in the email; the club's Strava event is linked from every ride's page.

### Importing the club's Strava events

The club's upcoming Strava events can show up on the calendar automatically:

1. Set up the Strava API application (below).
2. On the admin's **Rides** tab, press **Connect Strava** and approve with a Strava account that's in the club (a club admin is best).

From then on, the site reads the club's events every hour (and whenever you press **Import now**). An event that matches a weekly ride (same day, and the same name or start time) is linked to that weekly ride instead of being added twice. Leaders who signed up on the website are kept when an event changes. An event deleted on Strava leaves the calendar, unless leaders had signed up for it, in which case it's marked cancelled.

**Past rides.** Once a day the import also puts this year's past club events on the calendar, including those from before Strava was connected, so past months show the rides that happened. They're matched to weekly rides the same way, and each event's poster is its leader when the ride has no pace groups. Events already on the calendar are skipped, and each day is imported once, so a past ride an admin deletes doesn't come back. While the club's past events are known, a weekly ride's past date shows only if it was on Strava or someone signed up for it or changed it; upcoming weekly dates always show.

Each imported ride also gets what the Strava event says: the route (a map, distance, climbing, and riding time), the pace (which sets its level, see above), how many riders have joined, a Directions link to the start, and the ride notes. Whoever posted the event becomes the ride's leader, unless someone already signed up. The calendar lists show the level, distance, and riders going under each ride. Most club events are private on Strava, and some start at a member's house, so for those the public site leaves out the notes and a start that's a street address (including in the newsletter, search results, and the API). Riders signed in with Strava and admins see everything.

Once a day the import also reads the club's past events to build the **Club Routes** page (`/routes`, in the Info menu): every public route the club has ridden more than once, with its map and the next ride on the calendar that uses it. The Tuesday and Thursday Night Rides come first, each as one card that adds up all its routes (anything named TNR, Base Laps or ThNR). Other routes that look like the same ride (a place name in common, like "Steer Horn" and "Steerhorn Neck", and within 3 miles and 15% of the climbing) also share one card, with the others listed under it. The rest follow, most ridden first. Strava limits how often the site can ask, so new routes fill in about 25 an hour. Routes that are private on Strava stay off the page.

The admin's **Rides → History** page lists every club event Strava has (back to 2017): the date, who posted it, the route, and who joined it, with rides per year and who posted and joined the most. Strava keeps who joined an event for only about 16 months, so the import saves each list after the ride and keeps it for good (about 15 rides an hour while catching up). Joining is a sign-up on Strava, not proof someone rode. Only admins see this page.

Strava requires a paid subscription on the account that owns the API application. The connected account needs to stay subscribed, or the import stops with an "Application Inactive" error on the Rides tab.

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

The home page has an "On Instagram" section, set up in the admin portal under **Instagram**. Each newsletter issue can also show up to 3 posts.

**With the club's Instagram connected** (below), the site reads the latest posts every few hours and draws them itself: all the same size, with videos that play right on the page, a line of the caption, and a link to the post. With no posts picked, the home page shows the 3 latest, so it changes on its own as the club posts. Paste up to 6 post links to pick them instead (the admin tab lists the latest posts to copy from). The site keeps its own copy of each post it shows in `data/instagram` (a cache, left out of backups), since Instagram's links to the files expire. A reel that uses music from Instagram's library comes without its video (Instagram's rule), so it shows its cover and plays on Instagram.

**Without it**, picked posts use Instagram's own embed. Instagram's player usually won't start inside other websites, so videos play only on Instagram, and posts come out different heights. That embed also loads a script from Instagram, which Instagram may use to track visitors who are logged in; it loads only when the posts are about to scroll into view. A picked post that isn't among the feed's latest posts uses the embed too.

**Connecting Instagram** (once), through [Behold](https://behold.so), which reads Instagram for the site. (Meta's own API needs a Meta developer app and a business portfolio holding the Instagram account, and Meta wouldn't finish adding the account.)

1. In the Instagram app, make @paxvelo a **Business** or **Creator** account (Settings → Account type and tools → Switch to professional account). It's free and the account stays public. No Facebook Page is needed.
2. Sign up at https://behold.so (the free plan is enough) and connect @paxvelo as a source.
3. Make a feed of type **JSON** and copy its link (`https://feeds.behold.so/…`).
4. In the admin, open **Instagram**, paste the link, and press **Connect**.

Behold's free plan updates the feed once a day, shows the latest 6 posts, and allows 1,200 reads a month. The site reads it every 3 hours (about 240 a month), plus when you press **Check now** or save picks. Pick from those 6 (older posts use Instagram's embed). If the feed stops working, the Instagram tab shows the error; reconnect @paxvelo in Behold.

## Meeting minutes

Upload minutes in the admin portal under **Minutes**: the meeting date, a title, an optional one-line summary, and the file (PDF or Word, up to 20 MB). Minutes aren't listed on the website. Each file gets a random address (shown in the table), and anyone with that link can open it, so a newsletter can link to it. Search engines are asked not to index the files. PDFs open in the browser; Word files download.

Files are stored on the server in `data/files`. Like photos, they're not part of the **Download a full backup** file, so keep the originals.

## Membership dues

Dues are $35 (Individual) or $45 (Family) per calendar year, paid through PayPal. To set it up:

1. Create a free PayPal Business account for the club, if you don't have one.
2. In PayPal, go to **Pay Links and Buttons** and create two payment links: Individual for $35 and Family for $45.
3. Paste them into `paxvelo.conf` as `PAYPAL_INDIVIDUAL_URL` and `PAYPAL_FAMILY_URL`.

The Join page asks for a name, email, and phone number, an emergency contact (their name and phone), and the plan, and they have to tick a box agreeing to the club terms and sign the League of American Bicyclists insurance waiver by ticking its box and typing their name (the data file keeps when they agreed, as `terms_agreed`). The admin Members page lists all of them, and **Export CSV** includes them. Anyone can type any email into the form, so for someone already on the list it only fills in details that are still blank: members from before these fields existed can add their phone and emergency contact by joining again, but nobody can change what's already there. Someone who unsubscribed from club emails isn't signed up again by the form, which tells them to email the club. An admin can still add them back or change their details with **Add a member**.

**Insurance waivers.** The club's insurance needs every member to sign the League of American Bicyclists waiver once a year, January to December. New members sign it on the join form. Everyone else signs it on their membership page (the "My membership" link in every club email), which asks for this year's waiver until they've signed it: so each January, email members asking them to sign. They tick a box, print their name as the club has it, and sign in a box with a finger, stylus or mouse. The drawn signature is saved with the waiver as a small picture, in the data file, so the nightly backups include it. Someone who can't draw one can choose to sign with their typed name instead, which is also how the form works without JavaScript; the record says which. Riders under 18 can't sign or join on their own: a parent or legal guardian signs for them every year (the rider's name and age, the guardian's phone, and the waiver with its release for parents and guardians). New members can do it on the join form, in a section that opens for the few who need it, with room for three riders: their one signature signs their own waiver and one for each rider. On the membership page, they sign for one rider at a time, as many as they need. Other adults who ride on a Family membership (up to three besides the member) sign their own waiver: they're listed by name and email on the join form, or added on the member's page, and each gets an email with a private link to a page with just their waiver, once an admin marks the member's dues paid (like the welcome email, so the join form can't be used to email strangers; each year, marking the dues paid emails the adults who haven't signed that year's waiver yet) (the same `/membership?t=` address, with their own secret; "Send me my link" finds them by email too). They're kept with the member (`household`) and don't get club emails. The member's page shows whether each has signed this year, and can add or remove them, or email their link again once the dues are paid. The Waivers tab and Needs attention count them like members. Each signing is kept for good in the data file (`waivers`), with the date and time, the name typed, where they signed, their IP address, and which version of the text they signed (the texts are kept in `waiver_texts`), even if the member is deleted later. The one exception is junk, like a prank on the join form: the president can delete a waiver from the bottom of its signed copy. The admin's **Members → Waivers** tab lists a year's signed waivers and who hasn't signed, opens each signed copy to print or save as a PDF, and exports a year as a spreadsheet. **Needs attention** says when paid members haven't signed this year's waiver.

The waiver's text is in `web/waiver.html`, and the release for parents and guardians in `web/waiver-guardian.html`, both copied word for word from the club's old site, typos and all. It's a legal document, so change it only to match a new version from the League. Waivers already signed keep the text they were signed with.

When someone joins, they pick a plan and land on the right PayPal link, and every admin gets an email with their plan, phone, and riding answer. PayPal emails the club for each payment. Find that person on the admin Members page and press **Mark paid**. For someone who joined on the website, the first **Mark paid** sends their welcome email (Email tab), with the ride advice for their answer on the join form. Marking them paid again, in later years or after an undo, doesn't send it again, and members added by an admin or imported never get one. The **Not paid** filter shows who still owes. On January 1 everyone shows as not paid for the new year, so you can email just that group a reminder. Put `{pay_link}` in the message and each member gets their own plan's link.

The site never handles card details or connects to PayPal directly, so there's nothing to maintain. To change the prices, update `DUES_INDIVIDUAL` and `DUES_FAMILY` and the PayPal links. For a separate donation option, add a PayPal donation link as `DONATE_URL` and a "Support the club" link appears on the homepage.

## Stats

The admin's **Stats** screen shows how the site is used. It only keeps counts: the site never records who looked at a page or which member opened an email.

- **Visitors each week:** a chart of the last 12 weeks (Monday to Sunday, the striped bar is this week so far). Click a week to see its numbers: visitors, page views, how many were on a phone, each page's visitors and views, and which sites people followed a link from (Instagram, Google, Strava and so on).
- **Newsletter issues:** each issue's visitors and views, every week since it came out added together. `paxvelo.club/newsletter` counts as the issue it showed at the time.
- **Emails to members:** for each email sent from **Email members**, how many were opened, how many people clicked a link, and how many clicked each link.

**How pages are counted.** A few lines at the end of `web/static/site.js` tell the site which page was viewed, after the page has loaded, so visitors never wait for it. Search engines and other robots don't run it, so they aren't counted, and neither are admins who are logged in. A visitor is one phone or computer, counted once a week: the site scrambles their internet address and browser together with the week into a code, keeps the codes only until the week ends, and never stores the address itself. No cookie is set, so the site needs no cookie banner. Ride pages on the calendar count together as one line.

**How emails are counted.** Each member's copy of an email from **Email members** has a tiny invisible picture and links that pass through the site (`/e/…`) on the way to where they point, all marked with a random code made for that copy. The code is never saved with the member it went to, so the site can count that a copy was opened, but not whose it was. A click forwards straight on, and only to the link that was in the email. The member's own links (My membership, Unsubscribe) go direct. Opens are a rough guide: Apple Mail on iPhones and iPads loads every email's pictures on its own, so some emails count as opened without being read, and email apps that block pictures only count once a link is clicked. Clicks are the dependable number. Test emails, welcome emails, membership links and emails sent before this was added aren't counted, and the plain-text copy of an email (for the few email apps without pictures or formatting) has its links unchanged.

The counts are kept in memory and saved once a minute to `data/stats.json`, apart from the club data. That file isn't part of the nightly backups or **Download a full backup**; losing it only loses the counts.

## Email setup (Brevo)

The site sends email through [Brevo](https://www.brevo.com)'s SMTP service, from an address on the club's own domain (`news@paxvelo.cc`). That keeps club emails out of spam folders far better than sending from a Gmail address. Brevo's free plan allows 300 emails a day (check [their pricing page](https://www.brevo.com/pricing/) for the current limit), which is plenty for a club this size.

1. **Domain:** in Brevo, add your domain under **Senders, domains & dedicated IPs** and add the DKIM and DMARC records it shows to your DNS (Cloudflare). Wait until Brevo shows the domain as verified.
2. **SMTP login:** under **SMTP & API**, copy the login (it looks like `abc123@smtp-brevo.com`: this is *not* your own email address) and create an SMTP key.
3. **Settings:** in `paxvelo.conf` on the server:

   ```
   SMTP_HOST=smtp-relay.brevo.com
   SMTP_PORT=587
   SMTP_USER=the login from step 2
   SMTP_PASS=the SMTP key from step 2
   FROM_EMAIL=news@paxvelo.cc
   ```

   Then `systemctl restart paxvelo`.
4. **Try it:** in the admin, Email → **Send test** to yourself before sending to everyone.

A wrong `SMTP_USER` shows up as "535 Authentication failed". Every failed email is written to the server log with who it was for and why: `journalctl -u paxvelo | grep failed`.

Emails to members have an unsubscribe link, and Gmail and Yahoo show their own one-click "Unsubscribe" button for it. Emails that aren't to members (an admin's password link, the club's ride notices) don't. If sending fails three times in a row (wrong password, daily limit reached, Brevo down), a send to everyone pauses with the reason shown. Nobody is skipped: fix the problem, or wait until the next day if it was the limit, and press **Resume**.

Replies to club emails go to `news@paxvelo.cc`, which needs to forward somewhere. A free way is Cloudflare's **Email Routing** (Cloudflare dashboard, paxvelo.cc, Email), forwarding `news@` to PaxVeloClub@gmail.com.

## Putting it online

You need a small Ubuntu 24.04 server (about $5/month at RackNerd, Vultr, or similar) and access to the domain's DNS settings.

For now the site runs at **paxvelo.cc**, while the old Squarespace site keeps **paxvelo.club**. The two don't affect each other. The address is set in just two places: `deploy/Caddyfile` (HTTPS) and `SITE_URL` in `paxvelo.conf` (links in emails). When it's time, see "Moving to paxvelo.club" below.

1. **DNS:** point both `paxvelo.cc` and `www.paxvelo.cc` (A records) at the server's IP address. Leave the paxvelo.club DNS alone.
2. **One-time server setup:** `scp -r deploy root@SERVER:/root/ && ssh root@SERVER 'cd deploy && sh setup.sh'`
3. **Settings:** `scp paxvelo.conf root@SERVER:/opt/paxvelo/` then `ssh root@SERVER 'chown paxvelo /opt/paxvelo/paxvelo.conf && chmod 600 /opt/paxvelo/paxvelo.conf'`
4. **Publishing:** connect GitHub (below) so every saved change goes live by itself. To publish by hand instead, run `./deploy/deploy.sh root@SERVER`: it uses the same installer, so it also puts the old version back if the new one doesn't start.

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

The server copies the data file every night and keeps 30 days, and every Sunday it makes an archive of the uploaded photos and minutes files that it keeps for 8 weeks (all in `/opt/paxvelo/backups`). Copies on the server don't help if the server itself is lost, so a second job copies that folder to Google Drive every night at 3:30 am.

**Setting up the Google Drive copy** (once; `setup.sh` already installs the pieces):

1. On a computer with a web browser, install [rclone](https://rclone.org/downloads/) and run `rclone authorize drive --drive-scope drive.file`. Sign in with the club Google account (so the backups don't depend on one person) and approve. rclone prints a token starting with `{"access_token":`. Treat it like a password: don't paste it in chat or email.
2. On the server, create `/opt/paxvelo/rclone.conf` containing these three lines, with the token on the last one:

   ```
   [gdrive]
   type = drive
   scope = drive.file
   token = {"access_token":"…"}
   ```

   Then run `chown paxvelo:paxvelo /opt/paxvelo/rclone.conf && chmod 600 /opt/paxvelo/rclone.conf`.
3. Test it: `sudo -u paxvelo /usr/local/bin/paxvelo-offsite`. A **PaxVelo backups** folder should appear in the Drive with the backup files.

The nightly job is `/etc/cron.d/paxvelo-offsite`, which runs `deploy/paxvelo-offsite`. Because of the `drive.file` setting, the server can only see files it put there itself. If a copy fails, the server log says `BACKUP TO GOOGLE DRIVE FAILED`: look with `journalctl -t paxvelo-offsite`. Nothing emails you about it, so now and then check that the newest file in the Drive folder is recent. If Google stops accepting the token (say, the account password changed), repeat steps 1 and 2.

**Restoring:** download the newest `paxvelo-DATE.json` from the Drive folder (or take one from `/opt/paxvelo/backups`), then run:

```
systemctl stop paxvelo
cp paxvelo-DATE.json /opt/paxvelo/data/paxvelo.json
chown paxvelo:paxvelo /opt/paxvelo/data/paxvelo.json
systemctl start paxvelo
```

Stop the site first. A running site writes its own copy of the data on every change and would overwrite the file you put back. For the photos and minutes, unpack the newest `media-DATE.tgz` into `/opt/paxvelo/data`. As a second option, **Download a full backup** at the bottom of the admin Members page saves the data file to your computer.

## Locking down SSH

Bots try passwords on every public server all day, so the server should accept only SSH keys. After the first login with your key (`ssh-copy-id root@SERVER`, or paste the public key into `/root/.ssh/authorized_keys`):

```
scp deploy/lock-ssh.sh root@SERVER:/root/ && ssh root@SERVER 'sh lock-ssh.sh'
```

The script refuses to run if root has no key yet, so it can't lock you out by mistake. Keep your current session open and check that a second login works before you close it. After this, only computers with an authorized key can log in. To add another computer, add its public key to `/root/.ssh/authorized_keys` from one that already works, or from the browser console in the RackNerd control panel. Publishing from GitHub uses its own key and is not affected. The old settings are saved in `/root/sshd-backup`.

The server also restarts itself at 4 am on the nights a security update needs it. The site comes back on its own in about a minute.
