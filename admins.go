package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Admin accounts. Each admin logs in with their email address and their own
// password, and resets it by email. Adding an admin emails them a link to set
// their password, so nobody ever has to pass one around.
//
// Before the first account exists, the shared ADMIN_PASSWORD from
// paxvelo.conf logs in (with any name), so the first admin can set things up.
// After that it still works as an emergency key with any admin's email, for
// whoever runs the server, in case email stops working.

type AdminUser struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	PassHash     string     `json:"pass_hash,omitempty"`
	Gen          int        `json:"gen,omitempty"` // goes up with every new password, which logs out old sessions
	ResetHash    string     `json:"reset_hash,omitempty"`
	ResetExpires *time.Time `json:"reset_expires,omitempty"`
	AddedBy      string     `json:"added_by,omitempty"`
	Added        time.Time  `json:"added"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
	// The president is the one admin who can remove admins and delete members.
	President bool `json:"president,omitempty"`
	// SeenGuide: they've opened How the site works, or dismissed Home's welcome card.
	SeenGuide bool `json:"seen_guide,omitempty"`
}

// HasPassword reports whether they've set their password yet.
func (u AdminUser) HasPassword() bool { return u.PassHash != "" }

const (
	inviteLinkLife = 7 * 24 * time.Hour
	resetLinkLife  = time.Hour
	minPassword    = 10
)

// ---- Passwords ----

const pbkdf2Rounds = 600_000

func hashPassword(pw string) (string, error) {
	salt := randHex(16)
	key, err := pbkdf2.Key(sha256.New, pw, []byte(salt), pbkdf2Rounds, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Rounds, salt, base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || err2 != nil || rounds < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, []byte(parts[2]), rounds, len(want))
	return err == nil && hmac.Equal(got, want)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte("reset\x00" + token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---- Store ----

var errAdminExists = errors.New("there’s already an admin with that email")

func (s *Store) Admins() []AdminUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AdminUser, 0, len(s.d.Admins))
	for _, u := range s.d.Admins {
		out = append(out, *u)
	}
	slices.SortFunc(out, func(a, b AdminUser) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func (s *Store) HasAdmins() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.Admins) > 0
}

// HasAdminPassword reports whether any admin has chosen a password, which
// ends the setup password's login sessions (see sessionAdmin).
func (s *Store) HasAdminPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adminWhere(func(u *AdminUser) bool { return u.HasPassword() }) != nil
}

func (s *Store) adminWhere(match func(*AdminUser) bool) *AdminUser {
	for _, u := range s.d.Admins {
		if match(u) {
			return u
		}
	}
	return nil
}

func (s *Store) Admin(id int) (AdminUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id }); u != nil {
		return *u, true
	}
	return AdminUser{}, false
}

func (s *Store) AdminByEmail(email string) (AdminUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.adminWhere(func(u *AdminUser) bool { return u.Email == email }); u != nil {
		return *u, true
	}
	return AdminUser{}, false
}

func (s *Store) AddAdmin(name, email, addedBy string) (AdminUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adminWhere(func(u *AdminUser) bool { return u.Email == email }) != nil {
		return AdminUser{}, errAdminExists
	}
	s.d.NextAdmin = max(s.d.NextAdmin, 1)
	u := &AdminUser{ID: s.d.NextAdmin, Name: name, Email: email, AddedBy: addedBy, Added: time.Now().UTC()}
	s.d.NextAdmin++
	s.d.Admins = append(s.d.Admins, u)
	return *u, s.save()
}

// HasPresident reports whether one of the admins is marked as president.
func (s *Store) HasPresident() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adminWhere(func(u *AdminUser) bool { return u.President }) != nil
}

// SetPresident makes one admin the president, and the others not.
func (s *Store) SetPresident(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adminWhere(func(u *AdminUser) bool { return u.ID == id }) == nil {
		return errors.New("that admin was removed")
	}
	for _, u := range s.d.Admins {
		u.President = u.ID == id
	}
	return s.save()
}

func (s *Store) DeleteAdmin(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Admins = slices.DeleteFunc(s.d.Admins, func(u *AdminUser) bool { return u.ID == id })
	return s.save()
}

// NewResetLink makes a new password link for an admin (replacing any older
// one) and returns its secret. Only a hash of it is stored.
func (s *Store) NewResetLink(id int, life time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id })
	if u == nil {
		return "", errors.New("that admin was removed")
	}
	token := randHex(24)
	exp := time.Now().Add(life).UTC()
	u.ResetHash, u.ResetExpires = tokenHash(token), &exp
	return token, s.save()
}

// AdminByResetLink finds the admin a password link belongs to, if it's still good.
func (s *Store) AdminByResetLink(token string) (AdminUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return AdminUser{}, false
	}
	h := tokenHash(token)
	u := s.adminWhere(func(u *AdminUser) bool {
		return u.ResetHash != "" && subtle.ConstantTimeCompare([]byte(u.ResetHash), []byte(h)) == 1
	})
	if u == nil || u.ResetExpires == nil || time.Now().After(*u.ResetExpires) {
		return AdminUser{}, false
	}
	return *u, true
}

// SetAdminPassword saves a new password, uses up any password link, and logs
// out the admin's other sessions.
func (s *Store) SetAdminPassword(id int, hash string) (AdminUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id })
	if u == nil {
		return AdminUser{}, errors.New("that admin was removed")
	}
	u.PassHash, u.ResetHash, u.ResetExpires = hash, "", nil
	u.Gen++
	return *u, s.save()
}

func (s *Store) RecordAdminLogin(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id }); u != nil {
		now := time.Now().UTC()
		u.LastLogin = &now
		s.saveOrLog()
	}
}

// ---- Sessions ----

// accountSession is what the login cookie holds for an admin account: their
// ID and password generation, so removing them or a new password ends it.
func accountSession(u AdminUser) string { return fmt.Sprintf("#%d:%d", u.ID, u.Gen) }

// sessionAdmin turns a login cookie's contents into the admin's name. A
// session from the setup password holds just the name typed at login. It has
// the president's powers, so it only lasts until an admin has chosen a
// password; after that, everyone logs in with their own account.
func (a *App) sessionAdmin(who string) (AdminUser, bool) {
	rest, ok := strings.CutPrefix(who, "#")
	if !ok {
		if a.store.HasAdminPassword() {
			return AdminUser{}, false
		}
		return AdminUser{Name: who}, true
	}
	ids, gens, _ := strings.Cut(rest, ":")
	id, err := strconv.Atoi(ids)
	gen, err2 := strconv.Atoi(gens)
	u, found := a.store.Admin(id)
	if err != nil || err2 != nil || !found || u.Gen != gen {
		return AdminUser{}, false
	}
	return u, true
}

// currentAdmin is the logged-in admin's account (ID 0 for the setup password).
func (a *App) currentAdmin(r *http.Request) (AdminUser, bool) {
	who, ok := a.sessionValue(r)
	if !ok {
		return AdminUser{}, false
	}
	return a.sessionAdmin(who)
}

// canManage reports whether the logged-in admin may remove admins and delete
// members: only the president can, or someone logged in with the server's
// setup password (before any accounts existed).
func (a *App) canManage(r *http.Request) bool {
	u, ok := a.currentAdmin(r)
	return ok && (u.President || u.ID == 0)
}

// canNamePresident reports whether the logged-in admin may choose the
// president: the president can hand it on, and until there is one, any admin
// can name the first.
func (a *App) canNamePresident(r *http.Request) bool {
	return a.canManage(r) || (a.loggedIn(r) && !a.store.HasPresident())
}

// ---- Login, forgotten passwords, and password links ----

func (a *App) loginView(r *http.Request) adminView {
	v := adminView{Title: "Log in", SetupLogin: !a.store.HasAdmins()}
	v.Flash, v.Error = flashFrom(r)
	return v
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.loggedIn(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	a.renderAdmin(w, r, http.StatusOK, "login", a.loginView(r))
}

func samePassword(given, want string) bool {
	g, w := sha256.Sum256([]byte(given)), sha256.Sum256([]byte(want))
	return want != "" && subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	v := a.loginView(r)
	refuse := func(status int, msg string) {
		v.Error, v.Email = msg, r.FormValue("email")
		a.renderAdmin(w, r, status, "login", v)
	}
	if !a.limit.Allow("login:"+clientIP(r), 10, 15*time.Minute) {
		refuse(http.StatusTooManyRequests, "Too many attempts. Try again in 15 minutes.")
		return
	}
	pw := r.FormValue("password")
	if v.SetupLogin {
		if !samePassword(pw, a.cfg.AdminPassword) {
			refuse(http.StatusUnauthorized, "That password isn’t right.")
			return
		}
		name := cleanName(r.FormValue("name"))
		if name == "" {
			refuse(http.StatusBadRequest, "Enter your name, so others can see who edited what.")
			return
		}
		a.setSession(w, name, 30*24*3600)
		http.Redirect(w, r, "/admin/admins?ok=setup", http.StatusSeeOther)
		return
	}
	email, _ := cleanEmail(r.FormValue("email"))
	u, found := a.store.AdminByEmail(email)
	switch {
	case found && u.HasPassword() && checkPassword(u.PassHash, pw):
	case found && samePassword(pw, a.cfg.AdminPassword):
		// The server's emergency key (ADMIN_PASSWORD). It works for any admin, so it's logged.
		log.Printf("admin login with the setup password as %s", u.Email)
	case found && !u.HasPassword():
		refuse(http.StatusUnauthorized, "You haven’t set a password yet. Use the link in your invite email, or press “Forgot your password?” for a new one.")
		return
	default:
		refuse(http.StatusUnauthorized, "That email and password don’t match an admin account.")
		return
	}
	a.store.RecordAdminLogin(u.ID)
	a.setSession(w, accountSession(u), 30*24*3600)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) forgotPage(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, http.StatusOK, "forgot", adminView{Title: "Reset your password"})
}

// forgot emails a password link to an admin. It says the same thing whether
// or not the email belongs to an admin, so it can't be used to find out who is one.
func (a *App) forgot(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	v := adminView{Title: "Reset your password"}
	email, ok := cleanEmail(r.FormValue("email"))
	switch {
	case !ok:
		v.Error = "Enter the email address you log in with."
	case !a.limit.Allow("forgot:"+clientIP(r), 5, time.Hour) || !a.limit.Allow("forgot:"+email, 3, time.Hour):
		v.Error = "Too many reset emails. Try again in an hour."
	default:
		if u, found := a.store.AdminByEmail(email); found {
			if _, err := a.sendPasswordLink(u, false, ""); err != nil {
				v.Error = "The email didn’t send. Try again later, or ask another admin to send you a link from the Admins tab."
				break
			}
		}
		v.Flash = "If " + email + " belongs to an admin, a link to set a new password is on its way. It works for an hour."
	}
	a.renderAdmin(w, r, http.StatusOK, "forgot", v)
}

// sendPasswordLink emails an admin a link to set their password: an invite
// (for a new admin) or a reset. It returns the link, which the Admins tab
// shows in test mode, when emails only go to the server log.
func (a *App) sendPasswordLink(u AdminUser, invite bool, by string) (string, error) {
	life := resetLinkLife
	if invite {
		life = inviteLinkLife
	}
	token, err := a.store.NewResetLink(u.ID, life)
	if err != nil {
		return "", err
	}
	link := a.cfg.SiteURL + "/admin/password?t=" + url.QueryEscape(token)
	first := u.Name
	if f := strings.Fields(u.Name); len(f) > 0 {
		first = f[0]
	}
	subject, body := "Reset your PaxVelo admin password", fmt.Sprintf(
		"Hi %s,\n\nTo set a new password for the PaxVelo admin portal, open this link within the next hour:\n%s\n\nIf you didn’t ask for this, you can ignore this email. Your password hasn’t changed.\n\nPaxVelo",
		first, link)
	if invite {
		subject, body = "You’re a PaxVelo website admin", fmt.Sprintf(
			"Hi %s,\n\n%s made you an admin of the PaxVelo website. Choose your password here (the link works for 7 days):\n%s\n\nAfter that, log in at %s/admin with this email address. If you forget your password, press “Forgot your password?” on the login page.\n\nPaxVelo",
			first, or(by, "Another admin"), link, a.cfg.SiteURL)
	}
	return link, a.mailer.SendDirect(u.Name, u.Email, subject, body)
}

func (a *App) passwordPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	u, ok := a.store.AdminByResetLink(token)
	v := adminView{Title: "Choose a password", Token: token, Account: u}
	if !ok {
		v.Error = "That link has expired or was already used. Ask for a new one below."
		a.renderAdmin(w, r, http.StatusOK, "forgot", v)
		return
	}
	a.renderAdmin(w, r, http.StatusOK, "password", v)
}

func passwordProblem(pw, again string) string {
	switch {
	case len([]rune(pw)) < minPassword:
		return fmt.Sprintf("Use at least %d characters. A few words in a row is easy to remember and hard to guess.", minPassword)
	case pw != again:
		return "The two passwords don’t match."
	}
	return ""
}

// setPassword saves the password chosen from an invite or reset link, and logs them in.
func (a *App) setPassword(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	token := r.FormValue("t")
	u, ok := a.store.AdminByResetLink(token)
	if !ok {
		a.renderAdmin(w, r, http.StatusOK, "forgot", adminView{Title: "Reset your password", Error: "That link has expired or was already used. Ask for a new one below."})
		return
	}
	pw := r.FormValue("password")
	if p := passwordProblem(pw, r.FormValue("password2")); p != "" {
		a.renderAdmin(w, r, http.StatusBadRequest, "password", adminView{Title: "Choose a password", Token: token, Account: u, Error: p})
		return
	}
	hash, err := hashPassword(pw)
	if err == nil {
		u, err = a.store.SetAdminPassword(u.ID, hash)
	}
	if err != nil {
		a.renderAdmin(w, r, http.StatusInternalServerError, "password", adminView{Title: "Choose a password", Token: token, Account: u, Error: "Couldn’t save it. Try again."})
		return
	}
	a.store.RecordAdminLogin(u.ID)
	a.setSession(w, accountSession(u), 30*24*3600)
	http.Redirect(w, r, "/admin?ok=password-set", http.StatusSeeOther)
}

// ---- The Admins tab ----

func (a *App) adminsView(r *http.Request) adminView {
	v := adminView{Title: "Admins", Tab: "admins", Admins: a.store.Admins(), Live: a.mailer.live(),
		CanManage: a.canManage(r), CanNamePresident: a.canNamePresident(r)}
	v.Account, _ = a.currentAdmin(r)
	v.Flash, v.Error = flashFrom(r)
	return v
}

func (a *App) adminsPage(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, http.StatusOK, "admins", a.adminsView(r))
}

// afterLink goes back to the Admins tab. In test mode the email only went to
// the server log, so the page shows the link instead.
func (a *App) afterLink(w http.ResponseWriter, r *http.Request, code, link string) {
	if a.mailer.live() {
		back(w, r, "/admin/admins?ok="+code)
		return
	}
	v := a.adminsView(r)
	v.Flash, v.TestLink = flashText[code], link
	a.renderAdmin(w, r, http.StatusOK, "admins", v)
}

func (a *App) addAdmin(w http.ResponseWriter, r *http.Request) {
	name := cleanName(r.FormValue("name"))
	email, ok := cleanEmail(r.FormValue("email"))
	switch {
	case name == "":
		back(w, r, "/admin/admins?err=name")
		return
	case !ok:
		back(w, r, "/admin/admins?err=email")
		return
	}
	me, _ := a.adminName(r)
	u, err := a.store.AddAdmin(name, email, me)
	if errors.Is(err, errAdminExists) {
		back(w, r, "/admin/admins?err=admin-exists")
		return
	}
	if err != nil {
		back(w, r, "/admin/admins?err=save")
		return
	}
	link, err := a.sendPasswordLink(u, true, me)
	if err != nil {
		back(w, r, "/admin/admins?err=invite")
		return
	}
	a.afterLink(w, r, "invited", link)
}

func (a *App) adminFromPath(w http.ResponseWriter, r *http.Request) (AdminUser, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	u, ok := a.store.Admin(id)
	if !ok {
		back(w, r, "/admin/admins?err=save")
	}
	return u, ok
}

// sendAdminLink emails an admin a new invite or reset link.
func (a *App) sendAdminLink(w http.ResponseWriter, r *http.Request) {
	u, ok := a.adminFromPath(w, r)
	if !ok {
		return
	}
	me, _ := a.adminName(r)
	link, err := a.sendPasswordLink(u, !u.HasPassword(), me)
	if err != nil {
		back(w, r, "/admin/admins?err=invite")
		return
	}
	a.afterLink(w, r, "link-sent", link)
}

// removeAdmin takes away someone's admin access. Only the president can.
func (a *App) removeAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.canManage(r) {
		back(w, r, "/admin/admins?err=president-only")
		return
	}
	u, ok := a.adminFromPath(w, r)
	if !ok {
		return
	}
	if me, _ := a.currentAdmin(r); me.ID == u.ID {
		back(w, r, "/admin/admins?err=remove-self")
		return
	}
	if err := a.store.DeleteAdmin(u.ID); err != nil {
		back(w, r, "/admin/admins?err=save")
		return
	}
	back(w, r, "/admin/admins?ok=admin-removed")
}

// makePresident hands the president's role to another admin (or names the
// first president).
func (a *App) makePresident(w http.ResponseWriter, r *http.Request) {
	if !a.canNamePresident(r) {
		back(w, r, "/admin/admins?err=president-only")
		return
	}
	u, ok := a.adminFromPath(w, r)
	if !ok {
		return
	}
	if err := a.store.SetPresident(u.ID); err != nil {
		back(w, r, "/admin/admins?err=save")
		return
	}
	back(w, r, "/admin/admins?ok=president")
}

// changePassword lets a logged-in admin pick a new password.
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := a.currentAdmin(r)
	if u.ID == 0 {
		back(w, r, "/admin/admins")
		return
	}
	if !checkPassword(u.PassHash, r.FormValue("current")) {
		back(w, r, "/admin/admins?err=current")
		return
	}
	if p := passwordProblem(r.FormValue("password"), r.FormValue("password2")); p != "" {
		code := "short"
		if len([]rune(r.FormValue("password"))) >= minPassword {
			code = "mismatch"
		}
		back(w, r, "/admin/admins?err="+code)
		return
	}
	hash, err := hashPassword(r.FormValue("password"))
	if err == nil {
		u, err = a.store.SetAdminPassword(u.ID, hash)
	}
	if err != nil {
		back(w, r, "/admin/admins?err=save")
		return
	}
	a.setSession(w, accountSession(u), 30*24*3600)
	back(w, r, "/admin/admins?ok=password-changed")
}
