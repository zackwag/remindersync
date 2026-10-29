//go:build unix

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/lock"
)

// harness runs Run in the background and reports each sync on synced.
type harness struct {
	changes chan struct{}
	synced  chan context.Context
	results chan error
	logs    *syncBuilder
	cancel  context.CancelFunc
	done    chan error
}

// syncBuilder is a strings.Builder that's safe to write from Run's goroutine
// while the test reads it.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// start runs Run with o. Syncs return the queued errors in order, then nil.
func start(t *testing.T, o Options, queued ...error) *harness {
	t.Helper()
	h := &harness{
		changes: make(chan struct{}, 16),
		synced:  make(chan context.Context, 16),
		results: make(chan error, 16),
		logs:    &syncBuilder{},
		done:    make(chan error, 1),
	}
	for _, err := range queued {
		h.results <- err
	}
	o.Changes = h.changes
	o.Sync = func(ctx context.Context) error {
		h.synced <- ctx
		select {
		case err := <-h.results:
			return err
		default:
			return nil
		}
	}
	if o.Poll == 0 {
		o.Poll = time.Hour
	}
	if o.Debounce == 0 {
		o.Debounce = time.Hour
	}
	if o.Retry == 0 {
		o.Retry = time.Hour
	}
	o.Logger = slog.New(slog.NewTextHandler(h.logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

// waitSync waits for the next sync.
func (h *harness) waitSync(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-h.synced:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a sync")
		return nil
	}
}

// noSync asserts that no sync starts for a while.
func (h *harness) noSync(t *testing.T) {
	t.Helper()
	select {
	case <-h.synced:
		t.Fatal("unexpected sync")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSyncsAtStartAndOnPoll(t *testing.T) {
	h := start(t, Options{Poll: 20 * time.Millisecond})
	for range 3 {
		h.waitSync(t)
	}
}

func TestBurstOfChangesSyncsOnce(t *testing.T) {
	h := start(t, Options{Debounce: 30 * time.Millisecond})
	h.waitSync(t)
	h.noSync(t)

	for range 3 {
		h.changes <- struct{}{}
	}
	h.waitSync(t)
	h.noSync(t)
}

func TestHeldLockRetriesSooner(t *testing.T) {
	h := start(t, Options{Retry: 10 * time.Millisecond}, fmt.Errorf("sync: %w", lock.ErrHeld))
	h.waitSync(t)
	h.waitSync(t)
	h.noSync(t)
	if !strings.Contains(h.logs.String(), "another sync is running") {
		t.Errorf("logs = %s", h.logs.String())
	}
}

func TestFailedSyncIsLoggedAndWaitsForTheNextTrigger(t *testing.T) {
	h := start(t, Options{Debounce: 10 * time.Millisecond}, errors.New("1 of 1 pairs failed"))
	h.waitSync(t)
	h.noSync(t)
	if !strings.Contains(h.logs.String(), `msg="sync failed" err="1 of 1 pairs failed"`) {
		t.Errorf("logs = %s", h.logs.String())
	}
	h.changes <- struct{}{}
	h.waitSync(t)
}

func TestClosedWatchStops(t *testing.T) {
	h := start(t, Options{})
	h.waitSync(t)
	close(h.changes)
	if err := <-h.done; !errors.Is(err, ErrWatchStopped) {
		t.Errorf("err = %v, want ErrWatchStopped", err)
	}
	h.done <- nil // for the cleanup
}

func TestCancelStopsWithoutCancellingTheSync(t *testing.T) {
	h := start(t, Options{})
	ctx := h.waitSync(t)
	h.cancel()
	if err := <-h.done; err != nil {
		t.Errorf("err = %v", err)
	}
	h.done <- nil
	if ctx.Err() != nil {
		t.Error("the sync's context was cancelled with the daemon's")
	}
}

func TestReplacedBinaryStopsAfterTheSync(t *testing.T) {
	var replaced atomic.Bool
	h := start(t, Options{Debounce: 10 * time.Millisecond, Replaced: replaced.Load})
	h.waitSync(t)
	replaced.Store(true)
	h.changes <- struct{}{}
	h.waitSync(t)
	if err := <-h.done; !errors.Is(err, ErrReplaced) {
		t.Errorf("err = %v, want ErrReplaced", err)
	}
	h.done <- nil
}

func TestReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remindersync")
	if err := os.WriteFile(path, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Replaced(path + ".missing"); err == nil {
		t.Error("watching a missing file didn't fail")
	}
	replaced, err := Replaced(path)
	if err != nil {
		t.Fatal(err)
	}
	if replaced() {
		t.Fatal("replaced before anything changed")
	}

	// Swapped in, like an installer's rename or `brew upgrade`.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if !replaced() {
		t.Error("a swapped-in file wasn't noticed")
	}

	// Rewritten in place.
	if replaced, err = Replaced(path); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !replaced() {
		t.Error("a changed file wasn't noticed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !replaced() {
		t.Error("a removed file wasn't noticed")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestLogWriter(t *testing.T) {
	var out strings.Builder
	now := func() time.Time { return time.Date(2026, 10, 28, 15, 4, 5, 0, time.UTC) }
	w := LogWriter(&out, now)
	for _, s := range []string{"family: Reminders", ` "Family"` + "\n  Applied 1 change.\n", "partial"} {
		if n, err := w.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	want := "2026-10-28T15:04:05Z family: Reminders \"Family\"\n2026-10-28T15:04:05Z   Applied 1 change.\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}

	if _, err := LogWriter(failWriter{}, now).Write([]byte("x\n")); err == nil {
		t.Error("want the underlying write error")
	}
}
