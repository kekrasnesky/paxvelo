package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// linkFrom pulls the password link out of the Admins tab (test mode shows it).
func linkFrom(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, "/admin/password?t=")
	if i < 0 {
		t.Fatal("no password link on the page")
	}
	end := strings.IndexAny(page[i:], `"<`)
	u, _ := url.Parse(strings.ReplaceAll(page[i:i+end], "&amp;", "&"))
	return u.Query().Get("t")
}

func TestAdminAccounts(t *testing.T) {
	a, h, setup := testApp(t)

	// Before any account exists, the setup password logs in with a name.
	if w := do(t, h, nil, "GET", "/admin/login", "", nil); !strings.Contains(w.Body.String(), "Setup password") {
		t.Fatal("first login should ask for the setup password")
	}

	// Adding an admin "emails" them a link to choose a password.
	ct, body := form(url.Values{"name": {"Pat Rider"}, "email": {"Pat@Example.com"}})
	w := do(t, h, setup, "POST", "/admin/admins", ct, body)
	token := linkFrom(t, w.Body.String())
	if u, ok := a.store.AdminByEmail("pat@example.com"); !ok || u.HasPassword() || u.AddedBy != "Test Admin" {
		t.Fatalf("new admin: %+v", u)
	}
	ct, body = form(url.Values{"name": {"Pat again"}, "email": {"pat@example.com"}})
	if w := do(t, h, setup, "POST", "/admin/admins", ct, body); !strings.Contains(w.Header().Get("Location"), "err=admin-exists") {
		t.Errorf("duplicate admin: %s", w.Header().Get("Location"))
	}

	// Now the login page asks for an email, and Pat can't log in without a password.
	if w := do(t, h, nil, "GET", "/admin/login", "", nil); !strings.Contains(w.Body.String(), `name="email"`) {
		t.Error("login should ask for an email once accounts exist")
	}
	login := func(email, pw string) *http.Response {
		ct, body := form(url.Values{"email": {email}, "password": {pw}})
		return do(t, h, nil, "POST", "/admin/login", ct, body).Result()
	}
	if res := login("pat@example.com", "whatever-it-is"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("login before setting a password: %d", res.StatusCode)
	}

	// Choosing a password: too short, then good. The link only works once.
	ct, body = form(url.Values{"t": {token}, "password": {"short"}, "password2": {"short"}})
	if w := do(t, h, nil, "POST", "/admin/password", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("short password: %d", w.Code)
	}
	ct, body = form(url.Values{"t": {token}, "password": {"pedal to the metal"}, "password2": {"pedal to the metal"}})
	w = do(t, h, nil, "POST", "/admin/password", ct, body)
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) == 0 {
		t.Fatalf("set password: %d", w.Code)
	}
	pat := w.Result().Cookies()[0]
	if w := do(t, h, pat, "GET", "/admin/newsletter", "", nil); !strings.Contains(w.Body.String(), "Pat Rider") {
		t.Error("Pat isn't logged in after choosing a password")
	}
	if w := do(t, h, nil, "GET", "/admin/password?t="+token, "", nil); !strings.Contains(w.Body.String(), "expired or was already used") {
		t.Error("a used link still works")
	}

	// Logging in with the email and password; a wrong password is refused.
	if res := login("pat@example.com", "pedal to the meddle"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", res.StatusCode)
	}
	if res := login("PAT@example.com", "pedal to the metal"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("right password: %d", res.StatusCode)
	}
	// The setup password still works as the server's emergency key, with an admin's email.
	if res := login("pat@example.com", "test-password-123"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("emergency key: %d", res.StatusCode)
	}

	// Forgot password: the same answer whether or not the email is an admin's.
	ct, body = form(url.Values{"email": {"nobody@example.com"}})
	nobody := do(t, h, nil, "POST", "/admin/forgot", ct, body).Body.String()
	ct, body = form(url.Values{"email": {"pat@example.com"}})
	someone := do(t, h, nil, "POST", "/admin/forgot", ct, body).Body.String()
	if !strings.Contains(nobody, "If nobody@example.com belongs to an admin") || !strings.Contains(someone, "If pat@example.com belongs to an admin") {
		t.Error("forgot-password answers differ")
	}
	if u, _ := a.store.AdminByEmail("pat@example.com"); u.ResetHash == "" {
		t.Error("no reset link was made for Pat")
	}

	// A new password logs out Pat's old sessions.
	u, _ := a.store.AdminByEmail("pat@example.com")
	hash, _ := hashPassword("a whole new password")
	a.store.SetAdminPassword(u.ID, hash)
	if w := do(t, h, pat, "GET", "/admin/newsletter", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("old session still works after a new password: %d", w.Code)
	}

	// Removing an admin ends their sessions too.
	res := login("pat@example.com", "a whole new password")
	pat = res.Cookies()[0]
	do(t, h, setup, "POST", "/admin/admins/"+itoa(u.ID)+"/remove", "", nil)
	if w := do(t, h, pat, "GET", "/admin", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("removed admin is still logged in: %d", w.Code)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
