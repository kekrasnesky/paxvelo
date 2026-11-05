# PaxVelo club website

The club's website: public pages, membership signup with PayPal dues, a ride calendar, a newsletter, a photo gallery, and an admin portal for running it all. No monthly software fees beyond the server and domain.

**Log in to the admin portal at [paxvelo.club/admin](https://www.paxvelo.club/admin)** with your email and password. Everything below is done there, no coding needed.

## What you can do in the admin portal

| Tab | What it's for |
|---|---|
| **Home** | Where you land: how many members have paid, this week's rides, quick links to common jobs, and warnings if backups or security need a look. |
| **Pages** | Edit the words and photos on any page, or add a new page. |
| **Board** | Change board members, roles and photos. |
| **Members** | See who has joined and paid. Press **Mark paid** when PayPal emails you a payment (this also sends a new member their welcome email, once they have confirmed their email). Import or export lists. |
| **Members → Waivers** | See who has signed this year's insurance waiver. Each January, email members asking them to sign again. |
| **Rides / Calendar** | Set up weekly rides, add one-off events, change leaders, cancel rides. Approve rides that members submit. Tick **Virtual** for a Zwift ride (rides mentioning Zwift, virtual or a trainer ride are ticked automatically). |
| **Newsletter** | Build the monthly issue from blocks, preview it, publish it, then email members about it. |
| **Email → Send an email** | Send email to all members or a group (such as only those who haven't paid). Always **Send test** to yourself first. Pick **About**: club news and notices (dues, waivers) reach everyone; **Rides and events** skips members who turned those emails off. |
| **Email → Automations** | Emails the site sends by itself, like the welcome email. Green is on, red is off, yellow is sent by hand. Press one to see it, **Edit** its words, or flip its switch. **New automated email** adds your own, such as a reminder a few days after someone joins if they haven't paid. |
| **Website → Gallery** | Albums of ride photos. This is where the club's photos live: the newsletter picks its photos from here. Drag albums into order, **Feature** one to show it first and big, or **Hide** one to take it off the website without deleting it. |
| **Website → Photos to review** | Photos members shared wait here. Each person's photos are one card: press **Approve** to put them in the gallery, or **Reject**. Both can be undone. |
| **Instagram** | Choose which club posts show on the home page. |
| **Minutes** | Upload meeting minutes (PDF or Word). They aren't listed publicly; anyone with the link can open them. |
| **Treasurer** | Board members only. The club's books. **Expenses** and **Income**: add what was spent or received, with a photo or PDF of the receipt. Dues marked paid on Members count as membership income automatically. **Budget**: the year's starting balance, budget and notes. **Reports**: the Income/Expense report for board meetings, to print, download, or save for a meeting. Only the treasurer or the president can remove a line. |
| **Stats** | How many people visit and open emails. Counts only: it never tracks individuals. |
| **Security (gear icon)** | Add or remove admins, set the **Club PIN**, download a full backup. |

The **Needs attention** notice tells you when something is waiting: new photos, rides to approve, unsigned waivers.

## Common jobs

- **New member joined:** you get an email. When their PayPal payment arrives, find them under Members and press **Mark paid**. They're also sent a link to confirm their email; until they press it, Members shows "Email not confirmed" and club emails skip them.
- **Members checking their status:** **My Membership** is in the menu on every page. The first time, it asks for their email and sends a link; after they open it, that device remembers them.
- **Member renewing on the Join form:** nothing they typed counts until they press the link we email them, since anyone can type any email. Until then Members shows "Renewal not confirmed" and their waiver shows as not signed.
- **Start of each year:** everyone shows as not paid. Email that group a reminder (put `{pay_link}` in the message so each person gets their own PayPal link), and ask everyone to sign the new waiver.
- **Add an admin:** Security → Admins → enter name and email. They get a link to choose a password. Only the **President** can remove admins, delete members or remove an automated email. The President marks which admins are **board members** (only they see the Treasurer tab) and who is **Treasurer**.
- **Forgot your password:** use **Forgot your password?** on the login page.
- **Club PIN:** a shared code members can type to post photos and add rides without waiting for approval. Change it if it gets passed around.
- **Monthly newsletter:** Newsletter → Start a new draft → fill in the blocks → Save and preview → Save and publish → Email members about it.
- **Photos:** upload any size; the site shrinks them automatically.

## Who to ask

Anything the admin portal can't do (changing dues amounts or PayPal links, email setup, the server, backups, the Strava or Instagram connection) is handled by whoever looks after the website's technical side. Give them this repository. [CLAUDE.md](CLAUDE.md) has the technical details, and [content/README.md](content/README.md) explains page formatting.
