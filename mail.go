package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mailer sends email through any SMTP server (Brevo, in the standard setup).
// Switching providers later is a config change, not a code change.
type Mailer struct {
	log   *EmailLog // every email sent or tried (emaillog.go)
	cfg   Config
	store *Store
	site  *Site
	stats *Stats // counts opens and clicks on emails to members; nil in some tests
	wake  chan struct{}
	bg    sync.WaitGroup // emails sent in the background (Go), so tests can wait for them

	onNotify func(to, subject, body string) // lets tests see notices
	onSend   func(to, subject, body string) // lets tests see every email
}

// Go sends an email in the background, so the page that triggered it
// doesn't wait for the mail server. A failure is logged by send.
func (m *Mailer) Go(f func()) {
	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		f()
	}()
}

func newMailer(cfg Config, store *Store, site *Site) *Mailer {
	return &Mailer{cfg: cfg, store: store, site: site, wake: make(chan struct{}, 1), log: openEmailLog(cfg.DataDir)}
}

func (m *Mailer) live() bool { return m.cfg.SMTPHost != "" }

func (m *Mailer) mode() string {
	if !m.live() {
		return "test mode, emails are logged instead of sent"
	}
	return "sending through " + m.cfg.SMTPHost
}

// Wake tells the background sender there's new work.
func (m *Mailer) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// run sends queued campaign emails one at a time with a pause between each,
// which keeps you under the provider's limits and out of spam filters.
func (m *Mailer) run(ctx context.Context) {
	var streak []int // members that failed in a row
	streakCampaign := 0
	for {
		c, mem, ok := m.store.NextSend()
		if !ok {
			streak = nil
			select {
			case <-m.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		if c.ID != streakCampaign {
			streak, streakCampaign = nil, c.ID
		}
		err := m.sendCampaign(c.ID, mem, c.Subject, c.Body)
		m.store.RecordSend(c.ID, mem.ID, err)
		if err != nil {
			streak = append(streak, mem.ID)
			if len(streak) >= 3 {
				m.store.PauseCampaign(c.ID, "Paused after 3 failures in a row: "+err.Error(), streak)
				streak = nil
			}
		} else {
			streak = nil
		}
		select {
		case <-time.After(m.cfg.SendDelay):
		case <-ctx.Done():
			return
		}
	}
}

// personalize fills in the {placeholders} admins can use in emails.
func (m *Mailer) personalize(s string, mem Member) string {
	first, name := mem.FirstName(), mem.Name
	if first == "" {
		first, name = "there", "there"
	}
	pay := m.cfg.PayLink(mem.Plan)
	if pay == "" {
		pay = m.cfg.SiteURL + "/join"
	}
	s = strings.NewReplacer(
		"{first_name}", first,
		"{name}", name,
		"{site}", m.cfg.SiteURL,
		"{pay_link}", pay,
		"{dues}", "$"+strconv.Itoa(m.cfg.Dues(mem.Plan)),
		"{year}", strconv.Itoa(m.cfg.ThisYear()),
		"{ride_tips}", rideTips(m.store.Welcome(), mem, m.cfg.SiteURL),
		"{my_link}", m.cfg.membershipURL(mem.Token),
	).Replace(s)
	return extraBlankLines.ReplaceAllString(s, "\n\n") // where {ride_tips} came out empty
}

var extraBlankLines = regexp.MustCompile(`\n{3,}`)

// sendCampaign sends a member their copy of an email to members, marked so
// its opens and clicks are counted without saying whose it is (stats.go).
func (m *Mailer) sendCampaign(id int, mem Member, subject, body string) error {
	var track *emailTrack
	if m.stats != nil {
		track = newEmailTrack(m.stats, m.cfg.SiteURL, id, mem)
	}
	return m.sendTo(mailCampaign, id, mem, subject, body, track)
}

// sendTo sends one personalized email to a member, of a kind for the sent log.
func (m *Mailer) sendTo(kind string, campaign int, mem Member, subject, body string, track *emailTrack) error {
	links := memberLinks{Mine: m.cfg.membershipURL(mem.Token), Unsub: m.cfg.SiteURL + "/unsubscribe?t=" + url.QueryEscape(mem.Token), Track: track}
	return m.send(kind, campaign, mem.Name, mem.Email, m.personalize(subject, mem), m.personalize(body, mem), links)
}

func (m *Mailer) SendTest(to, subject, body string) error {
	return m.sendTo(mailTest, 0, Member{Email: to, Token: "test"}, "[Test] "+subject, body, nil)
}

// Notify emails a notice to the club (RIDES_EMAIL) in the background.
func (m *Mailer) Notify(subject, body string) {
	to := m.cfg.RidesEmail
	if m.onNotify != nil {
		m.onNotify(to, subject, body)
	}
	if to == "" {
		return
	}
	m.Go(func() { m.send(mailClub, 0, m.cfg.FromName, to, subject, body, memberLinks{}) })
}

// SendDirect sends one email that isn't to the member list, like an admin's
// password link, so it has no unsubscribe link. kind names it in the sent log.
func (m *Mailer) SendDirect(kind, toName, toEmail, subject, body string) error {
	return m.send(kind, 0, toName, toEmail, subject, body, memberLinks{})
}

// NotifySignup tells every admin that someone joined on the website, so the
// treasurer can look for their PayPal payment and mark them paid, which sends
// their welcome email. With no admin accounts yet it goes to ADMIN_EMAIL.
func (m *Mailer) NotifySignup(mem Member) {
	var to []AdminUser
	for _, u := range m.store.Admins() {
		if u.Email != "" {
			to = append(to, u)
		}
	}
	if len(to) == 0 && m.cfg.AdminEmail != "" {
		to = []AdminUser{{Name: m.cfg.FromName, Email: m.cfg.AdminEmail}}
	}
	year := m.cfg.ThisYear()
	riding := or(mem.LevelName(), "not answered")
	phone := or(mem.Phone, "not given")
	subject := "New member: " + mem.Name + " (" + mem.PlanName() + ")"
	body := "**" + mem.Name + "** just joined on the website.\n\n" +
		"- **Plan:** " + mem.PlanName() + ", $" + strconv.Itoa(m.cfg.Dues(mem.Plan)) + " for " + strconv.Itoa(year) + "\n" +
		"- **Email:** " + mem.Email + "\n" +
		"- **Phone:** " + phone + "\n" +
		"- **Riding:** " + riding + "\n\n" +
		"When their PayPal payment comes in, find them on the Members page and press **Mark paid**. That sends their welcome email.\n\n" +
		"[Open the Members page](" + m.cfg.SiteURL + "/admin/members?q=" + url.QueryEscape(mem.Email) + ")"
	for _, u := range to {
		m.SendDirect(mailSignup, u.Name, u.Email, subject, body) // a failure is logged by send
	}
}

// SendWelcome sends the welcome email, with the ride advice for their answer
// on the join form. It goes out when an admin first marks them paid.
func (m *Mailer) SendWelcome(mem Member) {
	w := m.store.Welcome()
	if !w.Enabled {
		return
	}
	body := w.Body
	if !strings.Contains(body, "{ride_tips}") {
		// A welcome email written before {ride_tips} existed: put the advice
		// before its last paragraph, the sign-off.
		if i := strings.LastIndex(body, "\n\n"); i >= 0 {
			body = body[:i] + "\n\n{ride_tips}" + body[i:]
		}
	}
	m.sendTo(mailWelcome, 0, mem, w.Subject, body, nil) // a failure is logged by send
}

// memberLinks are the member's own links at the foot of a club email: their
// membership page and unsubscribing. Track, on emails to members, marks the
// email so its opens and clicks are counted.
type memberLinks struct {
	Mine, Unsub string
	Track       *emailTrack
}

// send sends one email. links are empty for an email that isn't to the member
// list (a password link, a ride notice), which then has no unsubscribe link or
// one-click Unsubscribe button.
// Each one, sent or not, goes in the sent log (emaillog.go).
func (m *Mailer) send(kind string, campaign int, toName, toEmail, subject, text string, links memberLinks) error {
	entry := sentEmail{Time: time.Now().UTC(), Kind: kind, Campaign: campaign, To: toEmail, Name: toName, Subject: subject, Result: "sent"}
	htmlBody, err := m.site.renderEmail(subject, text, links, m.cfg.SiteURL)
	if err != nil {
		entry.Result, entry.Error = "failed", err.Error()
		m.log.Add(entry)
		return err
	}
	if m.onSend != nil {
		m.onSend(toEmail, subject, text)
	}
	plain := emailText(text, m.cfg.SiteURL) + "\n\n--\nPaxVelo, " + m.cfg.SiteURL + "\n"
	if links.Unsub != "" {
		plain += "My membership: " + links.Mine + "\nUnsubscribe: " + links.Unsub + "\n"
	}
	if !m.live() {
		log.Printf("[test mode, not sent] To: %s | Subject: %s\n%s\n", toEmail, subject, text)
		entry.Result = "test mode"
		m.log.Add(entry)
		return nil
	}
	msg := m.build(toName, toEmail, subject, plain, htmlBody, links.Unsub)
	// Every email comes through here, so a failure of any kind (a campaign,
	// a password link, a ride notice) shows in the server log with the reason.
	if err := m.deliver(toEmail, msg); err != nil {
		log.Printf("email to %s failed (%q): %v", toEmail, subject, err)
		entry.Result, entry.Error = "failed", err.Error()
		m.log.Add(entry)
		return err
	}
	m.store.CountEmail()
	m.log.Add(entry)
	return nil
}

func (m *Mailer) build(toName, toEmail, subject, text, htmlBody, unsubURL string) []byte {
	var b bytes.Buffer
	boundary := "pv-" + randHex(12)
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", (&mail.Address{Name: m.cfg.FromName, Address: m.cfg.FromEmail}).String())
	hdr("To", (&mail.Address{Name: toName, Address: toEmail}).String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", subject))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+randHex(16)+"@"+domainOf(m.cfg.FromEmail)+">")
	hdr("MIME-Version", "1.0")
	if unsubURL != "" {
		// One-click unsubscribe: Gmail and Yahoo show an "Unsubscribe" button from these.
		hdr("List-Unsubscribe", "<"+unsubURL+">")
		hdr("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{{"text/plain", text}, {"text/html", htmlBody}} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + part.ctype + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		qp.Write([]byte(part.body))
		qp.Close()
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

// deliver hands one message to the SMTP server. Port 465 uses TLS from the
// start; any other port must upgrade with STARTTLS before the password is sent.
func (m *Mailer) deliver(to string, msg []byte) error {
	host := m.cfg.SMTPHost
	addr := net.JoinHostPort(host, strconv.Itoa(m.cfg.SMTPPort))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	tlsConf := &tls.Config{ServerName: host}

	var conn net.Conn
	var err error
	if m.cfg.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConf)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(time.Minute))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	helo := "localhost"
	if u, err := url.Parse(m.cfg.SiteURL); err == nil && u.Hostname() != "" {
		helo = u.Hostname()
	}
	if err := c.Hello(helo); err != nil {
		return err
	}
	if m.cfg.SMTPPort != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				return err
			}
		} else if host != "localhost" && host != "127.0.0.1" {
			return errors.New("mail server doesn't support encryption (STARTTLS), so the password wasn't sent")
		}
	}
	if m.cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.cfg.FromEmail); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func domainOf(email string) string {
	if _, d, ok := strings.Cut(email, "@"); ok && d != "" {
		return d
	}
	return "localhost"
}
