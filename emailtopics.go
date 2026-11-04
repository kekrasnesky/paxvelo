package main

// What club emails a member gets. Unsubscribing stops everything; short of
// that, a member can turn off the newsletter, or emails about rides and
// events on the calendar, and still get club notices like dues reminders.
// Each email to members says which it is (Campaign.Topic): a newsletter
// issue, an admin's email about rides and events, or (no topic) club news.

const (
	topicNewsletter = "newsletter"
	topicEvents     = "events"
)

// cleanTopic keeps a topic members can turn off, or "" for club news.
func cleanTopic(s string) string {
	if s == topicNewsletter || s == topicEvents {
		return s
	}
	return ""
}

// topicName is how a topic reads on the unsubscribe page.
func topicName(topic string) string {
	switch topic {
	case topicNewsletter:
		return "the newsletter"
	case topicEvents:
		return "calendar event emails"
	}
	return ""
}

// Wants reports whether they still want emails on a topic they can turn off
// (club news, with no topic, always). Unsubscribing is checked separately
// (Emailable).
func (m Member) Wants(topic string) bool {
	switch topic {
	case topicNewsletter:
		return !m.NoNewsletter
	case topicEvents:
		return !m.NoEvents
	}
	return true
}

// SetTopics saves which topics a member wants, from their own page.
func (s *Store) SetTopics(token string, newsletter, events bool) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, false, nil
	}
	m.NoNewsletter, m.NoEvents = !newsletter, !events
	return *m, true, s.save()
}

// StopTopic turns one topic off, from the unsubscribe page or the one-click
// Unsubscribe button in an email of that topic.
func (s *Store) StopTopic(token, topic string) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, false, nil
	}
	switch topic {
	case topicNewsletter:
		m.NoNewsletter = true
	case topicEvents:
		m.NoEvents = true
	}
	return *m, true, s.save()
}
