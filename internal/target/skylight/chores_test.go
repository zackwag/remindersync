package skylight

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/engine"
	api "github.com/zackwag/remindersync/skylight"
)

const categoryID = "24197547"

type fakeChores struct {
	chores []api.Chore
	next   int

	choresErr, addErr, updateErr, deleteErr error

	queries []api.ChoreQuery
	adds    []api.NewChore
	updates []api.ChoreUpdate

	// stuck holds chores moved while completed. Like Skylight, reopening one
	// says pending in the response but leaves it completed in the list.
	stuck map[string]bool
}

func (f *fakeChores) add(ch api.Chore) {
	f.next++
	ch.ID = fmt.Sprint(f.next)
	f.chores = append(f.chores, ch)
}

func (f *fakeChores) Chores(_ context.Context, frame string, q api.ChoreQuery) ([]api.Chore, error) {
	if frame != frameID {
		return nil, fmt.Errorf("wrong frame %s", frame)
	}
	f.queries = append(f.queries, q)
	return slices.Clone(f.chores), f.choresErr
}

func (f *fakeChores) AddChore(_ context.Context, frame string, ch api.NewChore) (api.Chore, error) {
	if frame != frameID {
		return api.Chore{}, fmt.Errorf("wrong frame %s", frame)
	}
	f.adds = append(f.adds, ch)
	if f.addErr != nil {
		return api.Chore{}, f.addErr
	}
	f.add(api.Chore{Summary: ch.Summary, Start: ch.Start, CategoryID: ch.CategoryID, Status: api.ChorePending})
	return f.chores[len(f.chores)-1], nil
}

func (f *fakeChores) UpdateChore(_ context.Context, frame, id string, upd api.ChoreUpdate) (api.Chore, error) {
	if frame != frameID {
		return api.Chore{}, fmt.Errorf("wrong frame %s", frame)
	}
	f.updates = append(f.updates, upd)
	if f.updateErr != nil {
		return api.Chore{}, f.updateErr
	}
	if upd.Status != nil && (upd.Summary != nil || upd.Start != nil) {
		return api.Chore{}, errors.New("skylight: 400: either update the completion status, or update non-completion attributes")
	}
	i := slices.IndexFunc(f.chores, func(ch api.Chore) bool { return ch.ID == id })
	if upd.Summary != nil {
		f.chores[i].Summary = *upd.Summary
	}
	if upd.Start != nil {
		if f.chores[i].Status == api.ChoreComplete {
			if f.stuck == nil {
				f.stuck = make(map[string]bool)
			}
			f.stuck[id] = true
		}
		f.chores[i].Start = *upd.Start
	}
	if upd.Status != nil {
		if *upd.Status == api.ChorePending && f.stuck[id] {
			ch := f.chores[i]
			ch.Status = api.ChorePending
			return ch, nil
		}
		f.chores[i].Status = *upd.Status
	}
	return f.chores[i], nil
}

func (f *fakeChores) DeleteChore(_ context.Context, frame, _ string) error {
	if frame != frameID {
		return fmt.Errorf("wrong frame %s", frame)
	}
	return f.deleteErr
}

// noon on Oct 28 local time, so dates don't depend on the machine's zone.
func noon() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local) }

func TestChoreItems(t *testing.T) {
	f := &fakeChores{}
	f.add(api.Chore{Summary: "Pay rent", Start: "2026-11-01", Status: api.ChorePending, CategoryID: categoryID})
	f.add(api.Chore{Summary: "Haircut", Start: "2026-10-20", Status: api.ChoreComplete, CategoryID: categoryID})
	f.add(api.Chore{Summary: "Make bed", Start: "2026-10-28", Status: api.ChorePending, CategoryID: categoryID, Recurring: true})
	f.add(api.Chore{Summary: "Walk dog", Start: "2026-10-28", Status: api.ChorePending, CategoryID: categoryID, UpForGrabs: true})
	f.add(api.Chore{Summary: "Feed fish", Start: "2026-10-28", Status: api.ChorePending, CategoryID: "other"})

	got, err := NewChoreStore(f, frameID, categoryID, noon).Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.Item{
		{ID: "1", Title: "Pay rent", Due: "2026-11-01"},
		{ID: "2", Title: "Haircut", Completed: true, Due: "2026-10-20"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	wantQuery := api.ChoreQuery{After: "2026-09-28", Before: "2027-11-27", CategoryID: categoryID, IncludeLate: true}
	if f.queries[0] != wantQuery {
		t.Errorf("query = %+v, want %+v", f.queries[0], wantQuery)
	}
}

func TestChoreItemsErrors(t *testing.T) {
	f := &fakeChores{choresErr: errors.New("skylight: 500")}
	if _, err := NewChoreStore(f, frameID, categoryID, noon).Items(context.Background()); !errors.Is(err, f.choresErr) {
		t.Errorf("err = %v", err)
	}

	f = &fakeChores{chores: make([]api.Chore, MaxItems)}
	if _, err := NewChoreStore(f, frameID, categoryID, noon).Items(context.Background()); !errors.Is(err, ErrTruncated) {
		t.Errorf("err = %v, want ErrTruncated", err)
	}
}

func TestChoreCreate(t *testing.T) {
	ctx := context.Background()
	f := &fakeChores{}
	s := NewChoreStore(f, frameID, categoryID, noon)

	it, err := s.Create(ctx, engine.Item{ID: "R1", Title: "Pay rent", Due: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if it != (engine.Item{ID: "1", Title: "Pay rent", Due: "2026-11-01"}) {
		t.Errorf("returned %+v", it)
	}
	if _, err := s.Create(ctx, engine.Item{Title: "Haircut"}); err != nil {
		t.Fatal(err)
	}
	want := []api.NewChore{
		{Summary: "Pay rent", Start: "2026-11-01", CategoryID: categoryID},
		{Summary: "Haircut", Start: "2026-10-28", CategoryID: categoryID},
	}
	if !reflect.DeepEqual(f.adds, want) {
		t.Errorf("adds = %+v", f.adds)
	}

	f.addErr = errors.New("skylight: 422")
	if _, err := s.Create(ctx, engine.Item{Title: "x"}); !errors.Is(err, f.addErr) {
		t.Errorf("err = %v", err)
	}
}

func TestChoreUpdate(t *testing.T) {
	ctx := context.Background()
	f := &fakeChores{}
	f.add(api.Chore{Summary: "Pay rent", Start: "2026-11-01", Status: api.ChoreComplete, CategoryID: categoryID})
	s := NewChoreStore(f, frameID, categoryID, noon)

	no, due := false, "2026-11-08"
	it, err := s.Update(ctx, "1", engine.Change{Completed: &no, Due: &due})
	if err != nil {
		t.Fatal(err)
	}
	if it != (engine.Item{ID: "1", Title: "Pay rent", Due: "2026-11-08"}) {
		t.Errorf("returned %+v", it)
	}
	if len(f.updates) != 2 {
		t.Fatalf("sent %d updates, want the status and the date separately", len(f.updates))
	}
	if u := f.updates[0]; *u.Status != api.ChorePending {
		t.Errorf("first sent %+v, want the reopen first", u)
	}
	if f.chores[0].Status != api.ChorePending {
		t.Errorf("the chore is still %s in the list", f.chores[0].Status)
	}
	if u := f.updates[1]; u.Summary != nil || *u.Start != "2026-11-08" || u.Status != nil {
		t.Errorf("then sent %+v", u)
	}

	yes, title, empty := true, "Pay the rent", ""
	f.updates = nil
	if _, err := s.Update(ctx, "1", engine.Change{Title: &title, Completed: &yes, Due: &empty}); err != nil {
		t.Fatal(err)
	}
	if u := f.updates[0]; *u.Summary != "Pay the rent" || u.Start != nil {
		t.Errorf("sent %+v; an empty date should be left out", u)
	}
	if u := f.updates[1]; *u.Status != api.ChoreComplete {
		t.Errorf("then sent %+v, want the completion last", u)
	}

	f.updates = nil
	if _, err := s.Update(ctx, "1", engine.Change{Completed: &no}); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || *f.updates[0].Status != api.ChorePending {
		t.Errorf("sent %+v, want only the status", f.updates)
	}

	f.updates = nil
	it, err = s.Update(ctx, "1", engine.Change{Due: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || it.Title != "Pay the rent" {
		t.Errorf("sent %+v and returned %+v, want an empty update that returns the chore", f.updates, it)
	}

	f.updateErr = errors.New("skylight: 500")
	if _, err := s.Update(ctx, "1", engine.Change{Title: &title}); !errors.Is(err, f.updateErr) {
		t.Errorf("err = %v", err)
	}
}

func TestChoreDelete(t *testing.T) {
	ctx := context.Background()
	f := &fakeChores{}
	s := NewChoreStore(f, frameID, categoryID, noon)
	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	f.deleteErr = &api.APIError{StatusCode: http.StatusNotFound}
	if err := s.Delete(ctx, "1"); err != nil {
		t.Errorf("a chore that's already gone: err = %v", err)
	}
	f.deleteErr = &api.APIError{StatusCode: http.StatusInternalServerError}
	if err := s.Delete(ctx, "1"); err == nil {
		t.Error("want a 500 to be an error")
	}
}

func TestChoreStoreDefaultsNow(t *testing.T) {
	if s := NewChoreStore(&fakeChores{}, frameID, categoryID, nil); s.now == nil {
		t.Error("now wasn't defaulted")
	}
}
