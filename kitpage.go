package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The public /kit page: kit.md's words around the store (kit.go). From a
// member's own link (/kit?t=) the items get size boxes, and saving reserves
// them in this year's bulk order. The link is remembered in a cookie on
// /kit, like /membership's, so the page opens their order next time.

const kitCookie = "pv_kit"

type kitView struct {
	Page     *contentPage // content/kit.md
	Shop     KitShop
	Items    []KitItem // the ones on show
	Open     bool
	Token    string
	Member   *Member
	Lapsed   bool // their dues ran out: no member prices until they renew
	Test     bool // the link in a test email
	Invalid  bool // a link that isn't anyone's (any more)
	Order    *KitOrder
	Saved    bool
	Message  string
	MyLink   string // their membership page
	StoreURL string // the team store, open all year
}

// CanOrder: the form shows.
func (v kitView) CanOrder() bool {
	return v.Open && v.Member != nil && !v.Lapsed && (v.Order == nil || !v.Order.Paid())
}

// PayLink is where they pay their order's total.
func (v kitView) PayLink() string {
	if v.Order == nil || v.Order.Paid() {
		return ""
	}
	return v.Shop.PayLink(v.Order.Total())
}

func (a *App) kitToken(w http.ResponseWriter, r *http.Request) string {
	if t := r.URL.Query().Get("t"); t != "" {
		if t != testToken {
			if _, ok := a.store.MemberByToken(t); ok {
				a.rememberKit(w, t, 400*24*3600)
			}
		}
		return t
	}
	if c, err := r.Cookie(kitCookie); err == nil {
		return c.Value
	}
	return ""
}

func (a *App) rememberKit(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: kitCookie, Value: token, Path: "/kit", MaxAge: maxAge,
		HttpOnly: true, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

// kitView builds the page for a member's token ("" for a visitor).
func (a *App) kitView(token string) kitView {
	shop := a.store.Kit()
	v := kitView{Shop: shop, Open: shop.IsOpen(time.Now()), StoreURL: a.cfg.KitURL, Token: token}
	if p := a.site.contentPage("/kit"); p != nil {
		v.Page = p
	}
	for _, it := range shop.Items {
		if it.Shown() {
			v.Items = append(v.Items, *it)
		}
	}
	switch {
	case token == "":
	case token == testToken:
		v.Test = true
	default:
		m, ok := a.store.MemberByToken(token)
		if !ok {
			v.Invalid = true
			break
		}
		v.Member, v.Lapsed, v.MyLink = &m, !m.Paid(), a.cfg.membershipURL(m.Token)
		if o, ok := a.store.KitOrderFor(m.ID); ok {
			v.Order = &o
		}
	}
	return v
}

func (a *App) kitPage(w http.ResponseWriter, r *http.Request) {
	if a.site.contentPage("/kit") == nil { // the page was removed: the team store, as before
		http.Redirect(w, r, a.cfg.KitURL, http.StatusFound)
		return
	}
	t := a.kitToken(w, r)
	v := a.kitView(t)
	if v.Invalid && r.URL.Query().Get("t") == "" { // a remembered member since deleted
		a.rememberKit(w, "", -1)
		v = a.kitView("")
	}
	v.Saved = r.URL.Query().Get("saved") != ""
	a.renderKit(w, http.StatusOK, v)
}

func (a *App) renderKit(w http.ResponseWriter, status int, v kitView) {
	title := "Club Kit — " + siteName
	if v.Page != nil {
		title = v.Page.Title
	}
	a.site.render(w, status, "kit", "/kit", title, v)
}

// kitOrder saves what a member reserved: boxes named q-<item>-<size number>.
func (a *App) kitOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	r.ParseForm()
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	t := r.PostFormValue("t")
	v := a.kitView(t)
	if v.Test {
		a.renderKit(w, http.StatusOK, v)
		return
	}
	if v.Member == nil {
		v.Message = "That link doesn’t work any more. Open the newest kit email, or get a new link from My Membership."
		a.renderKit(w, http.StatusBadRequest, v)
		return
	}
	if v.Lapsed {
		a.renderKit(w, http.StatusBadRequest, v)
		return
	}
	want := map[int]map[string]int{}
	for _, it := range v.Items {
		for i, size := range it.Sizes {
			n, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("q-" + strconv.Itoa(it.ID) + "-" + strconv.Itoa(i))))
			if n > 0 {
				if want[it.ID] == nil {
					want[it.ID] = map[string]int{}
				}
				want[it.ID][size] = n
			}
		}
	}
	note := strings.TrimSpace(r.PostFormValue("note"))
	if len(note) > 300 {
		note = note[:300]
	}
	_, err := a.store.PlaceKitOrder(*v.Member, want, note, time.Now())
	switch {
	case errors.Is(err, errKitClosed):
		v.Message, v.Open = "The kit order just closed, so nothing changed. Email the club if you still need something.", false
		a.renderKit(w, http.StatusConflict, v)
		return
	case errors.Is(err, errKitPaid):
		v.Message = "Your order is paid, so it can’t change here. Email the club and we’ll sort it out."
		a.renderKit(w, http.StatusConflict, v)
		return
	case err != nil:
		log.Printf("kit order: %v", err)
		v.Message = "Something went wrong on our end. Try again in a minute."
		a.renderKit(w, http.StatusInternalServerError, v)
		return
	}
	http.Redirect(w, r, "/kit?t="+url.QueryEscape(t)+"&saved=1#order", http.StatusSeeOther)
}

// contentPage is a content page as parsed (its words, not the whole page).
func (s *Site) contentPage(path string) *contentPage {
	for _, p := range *s.pageList.Load() {
		if p.Path == path {
			return p
		}
	}
	return nil
}
