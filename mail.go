package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"html"
	"html/template"
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
	"time"
)

// Mailer sends email through any SMTP server (the club Gmail by default).
// Switching providers later is a config change, not a code change.
type Mailer struct {
	cfg   Config
	store *Store
	site  *Site
	wake  chan struct{}

	onNotify func(to, subject, body string) // lets tests see notices
}

func newMailer(cfg Config, store *Store, site *Site) *Mailer {
	return &Mailer{cfg: cfg, store: store, site: site, wake: make(chan struct{}, 1)}
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
// which keeps you under Gmail's limits and out of spam filters.
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
		err := m.SendTo(mem, c.Subject, c.Body)
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
	return strings.NewReplacer(
		"{first_name}", first,
		"{name}", name,
		"{site}", m.cfg.SiteURL,
		"{pay_link}", pay,
		"{dues}", "$"+strconv.Itoa(m.cfg.Dues(mem.Plan)),
		"{year}", strconv.Itoa(m.cfg.ThisYear()),
	).Replace(s)
}

// SendTo sends one personalized email to a member.
func (m *Mailer) SendTo(mem Member, subject, body string) error {
	unsub := m.cfg.SiteURL + "/unsubscribe?t=" + url.QueryEscape(mem.Token)
	return m.send(mem.Name, mem.Email, m.personalize(subject, mem), m.personalize(body, mem), unsub)
}

func (m *Mailer) SendTest(to, subject, body string) error {
	return m.SendTo(Member{Email: to, Token: "test"}, "[Test] "+subject, body)
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
	go m.send(m.cfg.FromName, to, subject, body, m.cfg.SiteURL) // a failure is logged by send
}

// SendDirect sends one email that isn't to the member list, like an admin's
// password link, so it has no unsubscribe link.
func (m *Mailer) SendDirect(toName, toEmail, subject, body string) error {
	return m.send(toName, toEmail, subject, body, m.cfg.SiteURL)
}

func (m *Mailer) SendWelcome(mem Member) {
	w := m.store.Welcome()
	if !w.Enabled {
		return
	}
	m.SendTo(mem, w.Subject, w.Body) // a failure is logged by send
}

func (m *Mailer) send(toName, toEmail, subject, text, unsubURL string) error {
	htmlBody, err := m.site.renderEmail(subject, text, unsubURL)
	if err != nil {
		return err
	}
	plain := text + "\n\n--\nPaxVelo, " + m.cfg.SiteURL + "\nUnsubscribe: " + unsubURL + "\n"
	if !m.live() {
		log.Printf("[test mode, not sent] To: %s | Subject: %s\n%s\n", toEmail, subject, text)
		return nil
	}
	msg := m.build(toName, toEmail, subject, plain, htmlBody, unsubURL)
	// Every email comes through here, so a failure of any kind (a campaign,
	// a password link, a ride notice) shows in the server log with the reason.
	if err := m.deliver(toEmail, msg); err != nil {
		log.Printf("email to %s failed (%q): %v", toEmail, subject, err)
		return err
	}
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
	// One-click unsubscribe: Gmail and Yahoo show an "Unsubscribe" button from these.
	hdr("List-Unsubscribe", "<"+unsubURL+">")
	hdr("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
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

var (
	paraBreak = regexp.MustCompile(`\n[ \t]*\n`)
	urlRe     = regexp.MustCompile(`https?://[^\s<>"']*[^\s<>"'.,;:!?)\]]`)
)

// textToHTML turns the plain text typed in the admin into email HTML:
// blank lines become paragraphs and web addresses become links.
func textToHTML(s string) template.HTML {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	var b strings.Builder
	for _, para := range paraBreak.Split(s, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		lines := strings.Split(para, "\n")
		for i, line := range lines {
			lines[i] = linkify(line)
		}
		b.WriteString(`<p style="margin:0 0 16px">` + strings.Join(lines, "<br>") + "</p>\n")
	}
	return template.HTML(b.String())
}

func linkify(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range urlRe.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:loc[0]]))
		u := html.EscapeString(s[loc[0]:loc[1]])
		b.WriteString(`<a href="` + u + `" style="color:#9a2b29">` + u + `</a>`)
		last = loc[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}
