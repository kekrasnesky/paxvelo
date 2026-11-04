package main

import "time"

// Renewing on the join form. Anyone can type any email into the public form,
// so when the address is already on the list, nothing they typed is used
// yet: no waiver, no riders under 18, no adults on a Family plan, no details.
// It's kept as a PendingJoin and the address is emailed a link to the
// member's page, where Confirm it's me applies it. Someone who typed a
// member's address without being them changes nothing, and learns nothing
// about the member either: the form says the same for every address on the
// list.

// PendingJoin is a join form filled in for an address already on the list,
// waiting for the member to confirm it was them.
type PendingJoin struct {
	At             time.Time        `json:"at"`
	Year           int              `json:"year"` // the dues year the waiver is for
	Phone          string           `json:"phone,omitempty"`
	EmergencyName  string           `json:"emergency_name,omitempty"`
	EmergencyPhone string           `json:"emergency_phone,omitempty"`
	Level          string           `json:"level,omitempty"`
	Terms          bool             `json:"terms,omitempty"`
	Signature      string           `json:"signature"`         // the name typed to sign the waiver
	Drawing        []byte           `json:"drawing,omitempty"` // the signature drawn, a PNG
	IP             string           `json:"ip,omitempty"`
	Riders         []PendingRider   `json:"riders,omitempty"` // riders under 18 they signed for
	Adults         []HouseholdAdult `json:"adults,omitempty"` // other adults on a Family plan (name and email)
}

type PendingRider struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// pendingJoinDays is how long a renewal waits for the member to confirm it.
const pendingJoinDays = 30

func (p *PendingJoin) fresh() bool {
	return p != nil && time.Since(p.At) < pendingJoinDays*24*time.Hour
}

// RenewalWaiting reports whether they renewed on the join form and haven't
// confirmed it yet.
func (m Member) RenewalWaiting() bool { return m.PendingJoin.fresh() }

func (p PendingJoin) details() memberDetails {
	return memberDetails{Phone: p.Phone, EmergencyName: p.EmergencyName, EmergencyPhone: p.EmergencyPhone, Level: p.Level, Terms: p.Terms}
}

// SetPendingJoin keeps a join form filled in for a member's address until
// they confirm it was them. A newer one replaces an older one.
func (s *Store) SetPendingJoin(id int, p PendingJoin) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return Member{}, nil
	}
	p.At = time.Now().UTC()
	m.PendingJoin = &p
	return *m, s.save()
}
