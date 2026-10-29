package alert

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/reminders"
	"github.com/zackwag/remindersync/skylight"
)

type fixture struct {
	now    time.Time
	alerts []string
	err    error
	t      *Tracker
}

func newFixture() *fixture {
	f := &fixture{now: time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)}
	f.t = &Tracker{
		After: 10 * time.Minute,
		Now:   func() time.Time { return f.now },
		Notify: func(subtitle, message string) error {
			f.alerts = append(f.alerts, subtitle+": "+message)
			return f.err
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	return f
}

func TestAlertsAfterFailingForAWhile(t *testing.T) {
	f := newFixture()
	timeout := errors.New("skylight: timeout")
	f.t.Report("family", timeout)
	f.now = f.now.Add(9 * time.Minute)
	f.t.Report("family", timeout)
	if len(f.alerts) != 0 {
		t.Fatalf("alerted before 10 minutes: %v", f.alerts)
	}

	f.now = f.now.Add(time.Minute)
	f.t.Report("family", timeout)
	f.now = f.now.Add(time.Minute)
	f.t.Report("family", timeout)
	if want := []string{"family isn't syncing: skylight: timeout"}; !slices.Equal(f.alerts, want) {
		t.Errorf("alerts = %v, want %v once", f.alerts, want)
	}

	f.t.Report("family", nil)
	f.t.Report("family", nil)
	if len(f.alerts) != 2 || f.alerts[1] != "family is syncing again: The last sync worked." {
		t.Errorf("alerts = %v, want one recovery", f.alerts)
	}
}

func TestBriefFailureDoesNotAlert(t *testing.T) {
	f := newFixture()
	f.t.Report("family", errors.New("skylight: 502"))
	f.now = f.now.Add(5 * time.Minute)
	f.t.Report("family", nil)

	// The next failure starts a new streak.
	f.now = f.now.Add(10 * time.Minute)
	f.t.Report("family", errors.New("skylight: 502"))
	if len(f.alerts) != 0 {
		t.Errorf("alerts = %v", f.alerts)
	}
}

func TestErrorsThatNeedYouAlertStraightAway(t *testing.T) {
	f := newFixture()
	f.t.Report("family", fmt.Errorf("plan: %w", engine.ErrTooManyChanges))
	f.t.Report("groceries", errors.New("skylight: timeout"))
	if len(f.alerts) != 1 || !strings.HasPrefix(f.alerts[0], "family isn't syncing") {
		t.Errorf("alerts = %v, want only family's", f.alerts)
	}
}

func TestNeedsYou(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("x: %w", engine.ErrTooManyChanges), true},
		{fmt.Errorf("x: %w", skylight.ErrAuth), true},
		{credentials.ErrNotFound, true},
		{reminders.ErrListNotFound, true},
		{reminders.ErrAmbiguousList, true},
		{fmt.Errorf("x: %w", &skylight.APIError{StatusCode: 404}), true},
		{&skylight.APIError{StatusCode: 403}, true},
		{&skylight.APIError{StatusCode: 500}, false},
		{errors.New("dial tcp: timeout"), false},
	}
	for _, tt := range tests {
		if got := NeedsYou(tt.err); got != tt.want {
			t.Errorf("NeedsYou(%v) = %t, want %t", tt.err, got, tt.want)
		}
	}
}

func TestFailedNotifyIsLoggedNotRetried(t *testing.T) {
	f := newFixture()
	f.err = errors.New("osascript: exit 1")
	f.t.Report("family", engine.ErrTooManyChanges)
	f.t.Report("family", engine.ErrTooManyChanges)
	if len(f.alerts) != 1 {
		t.Errorf("alerts = %v, want one try", f.alerts)
	}
}

func TestLongMessagesAreCut(t *testing.T) {
	f := newFixture()
	f.t.Report("family", errors.New(strings.Repeat("é", maxMessage+10)))
	f.now = f.now.Add(time.Hour)
	f.t.Report("family", errors.New(strings.Repeat("é", maxMessage+10)))
	if len(f.alerts) != 1 {
		t.Fatalf("alerts = %v", f.alerts)
	}
	body := []rune(strings.TrimPrefix(f.alerts[0], "family isn't syncing: "))
	if len(body) != maxMessage || body[len(body)-1] != '…' {
		t.Errorf("message is %d runes, want it cut to %d", len(body), maxMessage)
	}
}
