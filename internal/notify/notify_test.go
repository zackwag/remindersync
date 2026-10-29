package notify

import (
	"errors"
	"testing"
	"time"
)

// A test binary isn't an app bundle, so both calls refuse rather than reach
// UserNotifications, which throws for a process with no bundle.
func TestNeedsTheApp(t *testing.T) {
	if _, err := Authorize(time.Second); !errors.Is(err, ErrNoApp) {
		t.Errorf("Authorize err = %v, want ErrNoApp", err)
	}
	if err := Send("title", "body"); !errors.Is(err, ErrNoApp) {
		t.Errorf("Send err = %v, want ErrNoApp", err)
	}
}

func TestPermissionString(t *testing.T) {
	for p, want := range map[Permission]string{Pending: "pending", Allowed: "allowed", Denied: "denied"} {
		if got := p.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", p, got, want)
		}
	}
}
