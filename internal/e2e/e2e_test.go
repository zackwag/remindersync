//go:build integration && darwin

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/reminders"
	"github.com/zackwag/remindersync/internal/state"
	skylightstore "github.com/zackwag/remindersync/internal/target/skylight"
	"github.com/zackwag/remindersync/skylight"
)

func TestRealSync(t *testing.T) {
	ctx := context.Background()
	frameID, listID := os.Getenv("REMINDERSYNC_E2E_SKYLIGHT_FRAME"), os.Getenv("REMINDERSYNC_E2E_SKYLIGHT_LIST")
	if frameID == "" || listID == "" {
		t.Skip("set REMINDERSYNC_E2E_SKYLIGHT_FRAME and REMINDERSYNC_E2E_SKYLIGHT_LIST")
	}
	prefix := fmt.Sprintf("remindersync e2e %d", time.Now().Unix())

	ek, err := eventkit.New()
	if err != nil {
		t.Fatal(err)
	}
	list, err := ek.CreateList(eventkit.CreateListInput{Title: prefix, Source: "iCloud"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ek.DeleteList(list.ID); err != nil {
			t.Errorf("delete Reminders list %q: %v", prefix, err)
		}
	})

	creds, err := credentials.LoadSkylight()
	if err != nil {
		t.Fatal(err)
	}
	sky := skylight.NewClient(creds.Token,
		skylight.WithDeviceFingerprint(creds.DeviceFingerprint),
		skylight.WithTokenRefreshHook(func(tok skylight.Token) {
			creds.Token = tok
			if err := credentials.SaveSkylight(creds); err != nil {
				t.Errorf("save refreshed token: %v", err)
			}
		}))
	t.Cleanup(func() {
		items, err := sky.ListItems(ctx, frameID, listID)
		if err != nil {
			t.Errorf("list Skylight items for cleanup: %v", err)
			return
		}
		for _, it := range items {
			if strings.HasPrefix(it.Label, prefix) {
				if err := sky.DeleteListItem(ctx, frameID, listID, it.ID); err != nil {
					t.Errorf("delete leftover Skylight item %q: %v", it.Label, err)
				}
			}
		}
	})

	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	hub := reminders.NewStore(ek, list.ID)
	target := skylightstore.NewStore(sky, frameID, listID)
	pair := engine.Pair{ID: "e2e", Direction: engine.TwoWay, Hub: hub, Target: target}
	sync := func() engine.Result {
		t.Helper()
		res, err := engine.Sync(ctx, pair, db, engine.Options{})
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		for _, a := range res.Actions {
			t.Logf("  %s", a)
		}
		return res
	}
	find := func(s engine.Store, title string) (engine.Item, bool) {
		t.Helper()
		items, err := s.Items(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.Title == title {
				return it, true
			}
		}
		return engine.Item{}, false
	}

	milk := prefix + " milk"
	t.Log("reminder created in Reminders reaches Skylight")
	if _, err := hub.Create(ctx, milk); err != nil {
		t.Fatal(err)
	}
	sync()
	onFrame, ok := find(target, milk)
	if !ok || onFrame.Completed {
		t.Fatalf("Skylight item = %+v, %t; want an open %q", onFrame, ok, milk)
	}
	if res := sync(); len(res.Actions) != 0 {
		t.Fatalf("didn't settle: %v", res.Actions)
	}

	t.Log("checked off on Skylight completes the reminder")
	done := true
	if _, err := target.Update(ctx, onFrame.ID, engine.Change{Completed: &done}); err != nil {
		t.Fatal(err)
	}
	sync()
	if r, ok := find(hub, milk); !ok || !r.Completed {
		t.Fatalf("reminder = %+v, %t; want it completed", r, ok)
	}

	t.Log("renamed and reopened in Reminders updates Skylight")
	r, _ := find(hub, milk)
	oat, open := prefix+" oat milk", false
	if _, err := hub.Update(ctx, r.ID, engine.Change{Title: &oat, Completed: &open}); err != nil {
		t.Fatal(err)
	}
	sync()
	if it, ok := find(target, oat); !ok || it.Completed {
		t.Fatalf("Skylight item = %+v, %t; want an open %q", it, ok, oat)
	}

	t.Log("deleted in Reminders deletes from Skylight")
	if err := hub.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	sync()
	if it, ok := find(target, oat); ok {
		t.Fatalf("Skylight still has %+v", it)
	}
	if res := sync(); len(res.Actions) != 0 {
		t.Fatalf("didn't settle: %v", res.Actions)
	}
}
