package main

import (
	"net/http"
	"slices"
	"strings"
)

// Board roles. Website → Board lists who holds each club role, and each
// person there can be tied to their record on the member list. Admin access
// follows from it: an admin who is on the board sees the Treasurer tab, and
// the one whose role is Treasurer can remove its lines and saved reports.
// When roles change after an election, changing the Board page is all it
// takes. (The president's own powers stay with "Make president" on the
// Admins page, so editing a public page can't hand them on by mistake.)

// sameName compares names the way people type them: any case and spacing.
func sameName(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return a != "" && norm(a) == norm(b)
}

// boardRoles returns the Board page roles an admin holds. A board member tied
// to a member record matches the admin with that member's email or name; one
// not tied matches by the name shown on the Board page.
func (a *App) boardRoles(u AdminUser) []string {
	if u.ID == 0 {
		return nil
	}
	var out []string
	for _, b := range a.store.Board() {
		match := false
		if m, ok := a.store.MemberByID(b.Member); b.Member != 0 && ok {
			match = (m.Email != "" && strings.EqualFold(m.Email, u.Email)) || sameName(m.Name, u.Name)
		} else {
			match = sameName(b.Name, u.Name)
		}
		if match {
			out = append(out, or(b.Role, "Board"))
		}
	}
	return out
}

func isTreasurerRole(role string) bool { return strings.Contains(strings.ToLower(role), "treasurer") }

// canSeeMoney reports whether the logged-in admin may open the Treasurer
// tab: anyone on the Board page, the president, or the setup password.
func (a *App) canSeeMoney(r *http.Request) bool { return a.isBoard(r) }

// canRemoveMoney reports whether the logged-in admin may remove Treasurer
// lines and saved reports: the treasurer on the Board page, or whoever can
// manage (the president).
func (a *App) canRemoveMoney(r *http.Request) bool {
	if a.canManage(r) {
		return true
	}
	u, ok := a.currentAdmin(r)
	return ok && slices.ContainsFunc(a.boardRoles(u), isTreasurerRole)
}

// adminRoleTags are the Board page roles of each admin, for the Admins page
// (the president's own "President" is already shown).
func (a *App) adminRoleTags(admins []AdminUser) map[int]string {
	out := map[int]string{}
	for _, u := range admins {
		var roles []string
		for _, r := range a.boardRoles(u) {
			if !(u.President && strings.EqualFold(r, "President")) {
				roles = append(roles, r)
			}
		}
		if len(roles) > 0 {
			out[u.ID] = strings.Join(roles, ", ")
		}
	}
	return out
}

// linkBoard ties the people on the Board page to member records with the
// same name, once, when the link was added. After that admins pick the
// member on the Board page.
func (s *Store) linkBoard() {
	if s.d.BoardLinked {
		return
	}
	for i, b := range s.d.Board {
		if b.Member != 0 {
			continue
		}
		var found []int
		for _, m := range s.d.Members {
			if sameName(m.Name, b.Name) {
				found = append(found, m.ID)
			}
		}
		if len(found) == 1 {
			s.d.Board[i].Member = found[0]
		}
	}
	s.d.BoardLinked = true
}

// boardChoice is a member offered on the Board page.
type boardChoice struct {
	ID          int
	Name, Email string
	Admin       bool // has an admin login, so gets the Treasurer tab on the board
}

// boardChoices lists the members to pick from on the Board page, by name,
// noting who has an admin login.
func (a *App) boardChoices() []boardChoice {
	admins := a.store.Admins()
	var out []boardChoice
	for _, m := range a.store.Members() {
		if m.Name == "" {
			continue
		}
		c := boardChoice{ID: m.ID, Name: m.Name, Email: m.Email}
		c.Admin = slices.ContainsFunc(admins, func(u AdminUser) bool {
			return (m.Email != "" && strings.EqualFold(u.Email, m.Email)) || sameName(u.Name, m.Name)
		})
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y boardChoice) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	return out
}
