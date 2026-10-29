// Package alert says when a sync pair keeps failing, so the daemon doesn't
// fail quietly in the background for days.
package alert

import (
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
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
	// Notify shows an alert. [Notify] is the real one.
	Notify func(subtitle, message string) error
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

func (t *Tracker) send(subtitle, message string) {
	if err := t.Notify(subtitle, message); err != nil {
		t.Logger.Error("couldn't show an alert", "subtitle", subtitle, "err", err)
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

// run starts a command and waits for it. Tests replace it.
var run = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// Notify shows a macOS notification titled remindersync. It goes through
// osascript, which needs no app bundle, so it shows as coming from Script
// Editor. The text is passed as arguments rather than written into the
// script, so quotes in it can't break out.
func Notify(subtitle, message string) error {
	if r := []rune(message); len(r) > maxMessage {
		message = string(r[:maxMessage-1]) + "…"
	}
	return run("osascript",
		"-e", "on run argv",
		"-e", `display notification (item 2 of argv) with title "remindersync" subtitle (item 1 of argv)`,
		"-e", "end run",
		subtitle, message)
}
