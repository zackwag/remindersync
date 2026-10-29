// Package alert says when a sync pair keeps failing, so the daemon doesn't
// fail quietly in the background for days.
package alert

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/reminders"
	"github.com/zackwag/remindersync/skylight"
)

// maxMessage caps an alert's text. Notification Center shows a couple of
// lines, and an API error can carry a whole response body.
const maxMessage = 240

// Tracker turns sync outcomes into alerts: one when something has been
// failing for After, or on the first failure if the error needs someone to
// fix it, and one when it syncs again after an alert.
type Tracker struct {
	After time.Duration
	Now   func() time.Time
	// Notify shows an alert, such as [notify.Send].
	Notify func(title, body string) error
	Logger *slog.Logger

	failing map[string]*streak
}

type streak struct {
	since   time.Time
	alerted bool
}

// Report records the outcome of syncing name: a pair, or the config file.
func (t *Tracker) Report(name string, err error) {
	s := t.failing[name]
	if err == nil {
		if s != nil && s.alerted {
			t.send(name+" is syncing again", "The last sync worked.")
		}
		delete(t.failing, name)
		return
	}
	if s == nil {
		if t.failing == nil {
			t.failing = make(map[string]*streak)
		}
		s = &streak{since: t.Now()}
		t.failing[name] = s
	}
	if s.alerted || (!NeedsYou(err) && t.Now().Sub(s.since) < t.After) {
		return
	}
	s.alerted = true
	t.send(name+" isn't syncing", err.Error())
}

func (t *Tracker) send(title, body string) {
	if r := []rune(body); len(r) > maxMessage {
		body = string(r[:maxMessage-1]) + "…"
	}
	if err := t.Notify(title, body); err != nil {
		t.Logger.Error("couldn't show an alert", "title", title, "err", err)
	}
}

// NeedsYou reports whether err won't clear up on its own: the mass-change
// limit, a login that's expired or missing, or a list that's gone or can't be
// told apart from another.
func NeedsYou(err error) bool {
	for _, target := range []error{
		engine.ErrTooManyChanges,
		skylight.ErrAuth,
		credentials.ErrNotFound,
		reminders.ErrListNotFound,
		reminders.ErrAmbiguousList,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var apiErr *skylight.APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound)
}
