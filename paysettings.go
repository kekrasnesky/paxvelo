package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Card and PayPal setup on Treasurer → Payments: the Stripe keys and the
// PayPal links, typed in the admin portal instead of paxvelo.conf. Until
// they're saved once the conf's STRIPE_* and PAYPAL_*_URL are used; after
// that the saved ones are (payCfg). Only the treasurer or the president can
// change them. The keys never go back to a browser: the form shows the
// start and end of each.

type PaySettings struct {
	Set                 bool      `json:"set,omitempty"` // saved once: these replace paxvelo.conf's
	StripeSecretKey     string    `json:"stripe_secret_key,omitempty"`
	StripeWebhookSecret string    `json:"stripe_webhook_secret,omitempty"`
	PayPalIndividual    string    `json:"paypal_individual,omitempty"`
	PayPalFamily        string    `json:"paypal_family,omitempty"`
	By                  string    `json:"by,omitempty"`
	At                  time.Time `json:"at,omitzero"`
}

// payCfg is cfg with the payment settings saved on Payments in place of
// the conf's.
func payCfg(cfg Config, store *Store) Config {
	s := store.PaySettings()
	if s.Set {
		cfg.StripeSecretKey, cfg.StripeWebhookSecret = s.StripeSecretKey, s.StripeWebhookSecret
		cfg.PayPalIndividual, cfg.PayPalFamily = s.PayPalIndividual, s.PayPalFamily
	}
	return cfg
}

func (a *App) payCfg() Config { return payCfg(a.cfg, a.store) }

func (s *Store) PaySettings() PaySettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.PaySettings
}

func (s *Store) SetPaySettings(p PaySettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Set = true
	s.d.PaySettings = p
	return s.save()
}

// maskKey shows enough of a key to tell which one it is.
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 14 {
		return strings.Repeat("•", 8)
	}
	pre := k[:strings.LastIndex(k[:min(len(k), 9)], "_")+1]
	return pre + "…" + k[len(k)-4:]
}

// paySetupView is the setup form.
type paySetupView struct {
	Can                  bool   // the treasurer or the president
	Secret, Webhook      string // masked
	PayPalInd, PayPalFam string
	Hook                 string // the webhook address to give Stripe
	By                   string
	At                   time.Time
}

func (a *App) paySetupView(r *http.Request) paySetupView {
	c, s := a.payCfg(), a.store.PaySettings()
	return paySetupView{Can: a.canRemoveMoney(r), Secret: maskKey(c.StripeSecretKey), Webhook: maskKey(c.StripeWebhookSecret),
		PayPalInd: c.PayPalIndividual, PayPalFam: c.PayPalFamily, Hook: a.cfg.SiteURL + "/stripe/webhook", By: s.By, At: s.At}
}

// savePaySetup saves the setup form. A key left blank stays as it is; the
// secret key is tried on Stripe first, so a typo shows here and not when a
// member presses Pay.
func (a *App) savePaySetup(w http.ResponseWriter, r *http.Request) {
	const to = "/admin/treasurer/payments"
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	c := a.payCfg()
	p := PaySettings{StripeSecretKey: c.StripeSecretKey, StripeWebhookSecret: c.StripeWebhookSecret}
	if r.FormValue("remove") != "" {
		p.StripeSecretKey, p.StripeWebhookSecret = "", ""
	}
	secret := strings.TrimSpace(r.FormValue("secret"))
	hook := strings.TrimSpace(r.FormValue("webhook"))
	if secret != "" {
		if !strings.HasPrefix(secret, "sk_") && !strings.HasPrefix(secret, "rk_") {
			back(w, r, to+"?err=pay-secret#setup")
			return
		}
		var out struct{}
		if err := a.stripeAs(secret, "GET", "/v1/checkout/sessions?limit=1", nil, &out); err != nil {
			log.Printf("stripe: checking a new secret key: %v", err)
			back(w, r, to+"?err=pay-secret-no#setup")
			return
		}
		p.StripeSecretKey = secret
	}
	if hook != "" {
		if !strings.HasPrefix(hook, "whsec_") {
			back(w, r, to+"?err=pay-webhook#setup")
			return
		}
		p.StripeWebhookSecret = hook
	}
	for _, f := range []struct {
		name string
		to   *string
	}{{"paypal_individual", &p.PayPalIndividual}, {"paypal_family", &p.PayPalFamily}} {
		v := strings.TrimSpace(r.FormValue(f.name))
		if u, err := url.Parse(v); v != "" && (err != nil || u.Scheme != "https" || u.Host == "") {
			back(w, r, to+"?err=pay-paypal#setup")
			return
		}
		*f.to = v
	}
	p.By, _ = a.adminName(r)
	p.At = time.Now()
	if err := a.store.SetPaySettings(p); err != nil {
		back(w, r, to+"?err=save#setup")
		return
	}
	log.Printf("payments: card and PayPal setup changed by %s", or(p.By, "the setup password"))
	back(w, r, to+"?ok=pay-setup#setup")
}
