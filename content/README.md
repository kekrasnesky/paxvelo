# How to update the website

> **Easier option:** the admin portal at www.paxvelo.club/admin can edit every page (the **Pages** tab) and the board members with their photos (the **Board** tab), with no GitHub needed. A page edited there uses the admin version until someone presses "Go back to the GitHub version". This guide is for editing on GitHub, and its formatting tips apply in the admin too.

Every page of the PaxVelo website is a text file in this folder. Change the text, save, and the website updates by itself in about two minutes. You don't need to install anything.

| Page | File |
|---|---|
| Home | `home.md` |
| Our Rides | `rides.md` |
| Bike Safety 101 | `bike-safety.md` |
| Executive Board Members | `committee.md` |
| Club Kit (the **Shop Kit** link) | `kit.md` |

## Changing a page

1. Click the page's file in the list above this guide.
2. Click the **pencil icon** (top right of the file, "Edit this file").
3. Make your changes. The **Preview** tab shows roughly how it will look.
4. Click **Commit changes…**, write a few words about what you changed (like "Update Thursday ride start time"), and click **Commit changes** again.

That's it. About two minutes later the change is on www.paxvelo.club.

**If something's wrong with your edit**, the website stays as it was and GitHub emails you. The email says which file and line to fix, for example `content/rides.md: line 12: can't find the photo photos/thursday.jpg`. To check on a change, click the **Actions** tab at the top of the repository: a green check means it's live, a red X means it needs a fix.

**To undo a change**, open the file, click **History**, find the version you want, and copy its text back in.

## Writing text

| To get… | Type |
|---|---|
| A paragraph | Just type. Leave an empty line between paragraphs. |
| **Bold** | `**Bold**` |
| *Italics* | `*Italics*` |
| A link | `[the words to click](https://example.com)` |
| A link to another page on the site | `[Bike Safety 101](bike-safety.md)` or `[Bike Safety 101](/bike-safety)` |
| A big button | Put a link on a line by itself |
| A row of links | Put two or more links on one line, with nothing else |
| A note only editors can see | `<!-- like this -->` |

Links to other websites open in a new tab automatically.

## Headings and sections

```
# Page Title

The first paragraph after the title is shown a little larger.

## A Section

### A Column
```

- Each page has exactly one `#` title line, at the top.
- `##` starts a new section. On the home page, each section is a full-width band with its own background.
- `###` makes columns. Several `###` headings in a row sit side by side (stacked on phones). On the home page, that's how "Ride With Us" and "Giving Back" work.

## Lists

**Numbered list:**

```
1. Wear a helmet.
2. Ride with traffic.
```

**Bulleted list:**

```
- Water bottle
- Spare tube
```

**Fact table** (like the ride details on Our Rides): start every line with a bold label that ends in a colon.

```
- **Pace:** 20–24 mph
- **Start:** Park & Ride by NAS Pax Gate 2
```

**People cards** (the board page uses the admin's **Board** tab instead, which handles photos and framing): start every line with a bold name, then the role. To add a photo, put it at the start of the line. This only works on pages with `people: yes` in their settings (see "Adding a new page"), so ordinary lists elsewhere stay lists.

```
- **Jane Smith** President
- ![](photos/board/sam.webp) **Sam Lee** Treasurer
```

## Photos

Photos live in the `photos` folder inside this one.

- **To add a photo:** open the `photos` folder, click **Add file → Upload files**, drag the photo in, and click **Commit changes**. Then put it on a page with a line like `![What the photo shows](photos/my-photo.webp)`. The words in the square brackets describe the photo for people using screen readers.
- **A row of photos:** put two or more photos on one line, with nothing else. They sit side by side (swipeable on phones), cropped to the same 4:3 shape. Like the row on the home page. PNG pictures aren't cropped, for product pictures like the jersey and bibs on the kit page: make them the same size so they line up.
- **To replace a photo:** upload a new one with exactly the same file name. It replaces the old one everywhere.
- **The big photo at the top of the home page** is `photos/hero.webp` (wide, about 2000px across) and `photos/hero-phone.webp` (square, used on phones).

File names are case-sensitive: `Ride.webp` and `ride.webp` are different files. Keep photos small so the site stays fast: around 1000–2000px wide, saved as WebP at https://squoosh.app (quality 75). JPGs work too.

## Club settings in the text

These words in curly brackets are filled in automatically from the club's settings, so they stay correct everywhere if the settings change:

| Type | Becomes |
|---|---|
| `{strava}` | The club's Strava page (use it as a link: `[Find a ride]({strava})`) |
| `{kit}` | The team kit store |
| `{donate}` | The donation link. If none is set up, links to it are left out. |
| `{dues-individual}` | The Individual dues amount, like 35 |
| `{dues-family}` | The Family dues amount, like 45 |
| `{membership-plans}` | On a line by itself: the two Join boxes with prices |
| `{social-links}` | On a line by itself: the Discord, Strava, Facebook, and Instagram icons |
| `{instagram}` | On a line by itself: the Instagram posts picked on the admin's Instagram tab, and a Follow button |

The dues amounts, PayPal links, and Strava and kit addresses themselves are server settings. Ask whoever looks after the server to change those.

## Adding a new page

1. In this folder, click **Add file → Create new file**.
2. Name it with lowercase letters and dashes, ending in `.md`. The name becomes the web address: `group-ride-faq.md` is www.paxvelo.club/group-ride-faq.
3. Start it like this, then write the page below:

```
---
menu: Group Ride FAQ
order: 4
---

# Group Ride FAQ

Your text here.
```

The lines between the `---` marks are the page's settings, and all of them are optional:

| Setting | What it does |
|---|---|
| `menu` | Adds the page to the **Info** menu with this label. Leave it out to keep the page off the menu. |
| `order` | Where it goes in the Info menu: lower numbers come first. |
| `title` | The name in the browser tab, if it should differ from the `#` title. |
| `description` | The summary search engines and link previews show. |
| `wide` | `wide: yes` gives the page a wider layout, like the board page. |
| `people` | `people: yes` shows lists that start with a bold name as people cards, like the board page. |

To remove a page, open its file, click the **⋯** menu, and choose **Delete file**. If other pages link to it, update those links too.

## What isn't in this folder

- **The Join page and its form** are part of the membership system, in `web/templates/join.html`.
- **The header, menu, footer, and social links** are in `web/templates/layout.html`.
- **Colors and fonts** are in `web/static/site.css`.
- **Members, dues, and club emails** are managed in the admin portal at www.paxvelo.club/admin, not here.
