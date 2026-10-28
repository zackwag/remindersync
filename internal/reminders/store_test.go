package reminders

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/engine"
)

const listID = "LIST-GROCERIES"

var created = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// fakeClient is an in-memory stand-in for go-eventkit.
type fakeClient struct {
	lists     []eventkit.List
	reminders []eventkit.Reminder
	next      int

	listsErr, remindersErr, reminderErr, createErr, updateErr error
	// createInList, if set, is the list ID new reminders land in, regardless
	// of the name asked for.
	createInList string

	creates []eventkit.CreateReminderInput
	updates map[string]eventkit.UpdateReminderInput
	deletes []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		lists: []eventkit.List{
			{ID: listID, Title: "Groceries"},
			{ID: "LIST-FAMILY", Title: "Family"},
		},
		updates: make(map[string]eventkit.UpdateReminderInput),
	}
}

func (f *fakeClient) add(list, title string, completed bool) eventkit.Reminder {
	f.next++
	r := eventkit.Reminder{ID: fmt.Sprintf("R%d", f.next), Title: title, ListID: list, Completed: completed, CreatedAt: &created}
	f.reminders = append(f.reminders, r)
	return r
}

func (f *fakeClient) Lists() ([]eventkit.List, error) {
	return slices.Clone(f.lists), f.listsErr
}

func (f *fakeClient) Reminders(opts ...eventkit.ListOption) ([]eventkit.Reminder, error) {
	if len(opts) != 0 {
		return nil, errors.New("the store should filter by list itself")
	}
	return slices.Clone(f.reminders), f.remindersErr
}

func (f *fakeClient) Reminder(id string) (*eventkit.Reminder, error) {
	if f.reminderErr != nil {
		return nil, f.reminderErr
	}
	i := slices.IndexFunc(f.reminders, func(r eventkit.Reminder) bool { return r.ID == id })
	if i < 0 {
		return nil, eventkit.ErrNotFound
	}
	r := f.reminders[i]
	return &r, nil
}

func (f *fakeClient) CreateReminder(input eventkit.CreateReminderInput) (*eventkit.Reminder, error) {
	f.creates = append(f.creates, input)
	if f.createErr != nil {
		return nil, f.createErr
	}
	list := f.createInList
	if list == "" {
		for _, l := range f.lists {
			if l.Title == input.ListName {
				list = l.ID
			}
		}
	}
	r := f.add(list, input.Title, false)
	f.reminders[len(f.reminders)-1].DueDate = input.DueDate
	r.DueDate = input.DueDate
	return &r, nil
}

func (f *fakeClient) UpdateReminder(id string, input eventkit.UpdateReminderInput) (*eventkit.Reminder, error) {
	f.updates[id] = input
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	i := slices.IndexFunc(f.reminders, func(r eventkit.Reminder) bool { return r.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("reminders: reminder not found: %s", id)
	}
	if input.Title != nil {
		f.reminders[i].Title = *input.Title
	}
	if input.Completed != nil {
		f.reminders[i].Completed = *input.Completed
	}
	if input.DueDate != nil {
		f.reminders[i].DueDate = input.DueDate
	}
	if input.ClearDueDate {
		f.reminders[i].DueDate = nil
	}
	r := f.reminders[i]
	return &r, nil
}

func (f *fakeClient) DeleteReminders(ids []string) map[string]error {
	f.deletes = append(f.deletes, ids...)
	f.reminders = slices.DeleteFunc(f.reminders, func(r eventkit.Reminder) bool { return slices.Contains(ids, r.ID) })
	if slices.Contains(ids, "LOCKED") {
		return map[string]error{"LOCKED": errors.New("reminder is read-only")}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func TestItems(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient()
	c.add(listID, "Milk", false)
	c.add("LIST-FAMILY", "Pay mortgage", false)
	c.add(listID, "Eggs", true)
	noDate := c.add(listID, "Bread", false)
	c.reminders[len(c.reminders)-1].CreatedAt = nil

	items, err := NewStore(c, listID).Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.Item{
		{ID: "R1", Title: "Milk", CreatedAt: created},
		{ID: "R3", Title: "Eggs", Completed: true, CreatedAt: created},
		{ID: noDate.ID, Title: "Bread"},
	}
	if !slices.Equal(items, want) {
		t.Errorf("got  %+v\nwant %+v", items, want)
	}
}

func TestItemsErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("eventkit failed")

	t.Run("missing list is an error, not an empty list", func(t *testing.T) {
		c := newFakeClient()
		c.add(listID, "Milk", false)
		c.lists = c.lists[1:]
		if _, err := NewStore(c, listID).Items(ctx); !errors.Is(err, ErrListNotFound) {
			t.Errorf("err = %v, want ErrListNotFound", err)
		}
	})
	t.Run("lists", func(t *testing.T) {
		c := newFakeClient()
		c.listsErr = boom
		if _, err := NewStore(c, listID).Items(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("reminders", func(t *testing.T) {
		c := newFakeClient()
		c.remindersErr = boom
		if _, err := NewStore(c, listID).Items(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		s := NewStore(newFakeClient(), listID)
		if _, err := s.Items(canceled); !errors.Is(err, context.Canceled) {
			t.Errorf("Items err = %v", err)
		}
		if _, err := s.Create(canceled, engine.Item{Title: "x"}); !errors.Is(err, context.Canceled) {
			t.Errorf("Create err = %v", err)
		}
		if _, err := s.Update(canceled, "x", engine.Change{}); !errors.Is(err, context.Canceled) {
			t.Errorf("Update err = %v", err)
		}
		if err := s.Delete(canceled, "x"); !errors.Is(err, context.Canceled) {
			t.Errorf("Delete err = %v", err)
		}
	})
}

func TestCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("uses the list's current name", func(t *testing.T) {
		c := newFakeClient()
		c.lists[0].Title = "Groceries 🛒"
		it, err := NewStore(c, listID).Create(ctx, engine.Item{Title: "Milk"})
		if err != nil {
			t.Fatal(err)
		}
		if c.creates[0].ListName != "Groceries 🛒" || it.Title != "Milk" || it.Completed {
			t.Errorf("create %+v returned %+v", c.creates[0], it)
		}
	})

	t.Run("refuses a name another list shares", func(t *testing.T) {
		c := newFakeClient()
		c.lists = append(c.lists, eventkit.List{ID: "LIST-OLD", Title: "groceries"})
		_, err := NewStore(c, listID).Create(ctx, engine.Item{Title: "Milk"})
		if !errors.Is(err, ErrAmbiguousList) {
			t.Fatalf("err = %v, want ErrAmbiguousList", err)
		}
		if len(c.creates) != 0 {
			t.Errorf("created %v anyway", c.creates)
		}
	})

	t.Run("cleans up a reminder that landed in the wrong list", func(t *testing.T) {
		c := newFakeClient()
		c.createInList = "LIST-FAMILY"
		_, err := NewStore(c, listID).Create(ctx, engine.Item{Title: "Milk"})
		if err == nil {
			t.Fatal("want an error")
		}
		if len(c.reminders) != 0 || len(c.deletes) != 1 {
			t.Errorf("reminders %v, deletes %v; want the stray one deleted", c.reminders, c.deletes)
		}
	})

	t.Run("errors", func(t *testing.T) {
		boom := errors.New("eventkit failed")
		c := newFakeClient()
		c.createErr = boom
		if _, err := NewStore(c, listID).Create(ctx, engine.Item{Title: "Milk"}); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		c = newFakeClient()
		c.lists = nil
		if _, err := NewStore(c, listID).Create(ctx, engine.Item{Title: "Milk"}); !errors.Is(err, ErrListNotFound) {
			t.Errorf("err = %v, want ErrListNotFound", err)
		}
	})
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient()
	r := c.add(listID, "Milk", false)
	s := NewStore(c, listID)

	it, err := s.Update(ctx, r.ID, engine.Change{Completed: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.updates[r.ID]; got.Title != nil || got.Completed == nil || !*got.Completed {
		t.Errorf("sent %+v, want only Completed=true", got)
	}
	if !it.Completed || it.Title != "Milk" {
		t.Errorf("returned %+v", it)
	}

	// Everything the engine doesn't sync must be left out of the update, so
	// it's never cleared.
	got := c.updates[r.ID]
	if got.Notes != nil || got.DueDate != nil || got.Priority != nil || got.URL != nil || got.Tags != nil || got.Alarms != nil || got.ListName != nil ||
		got.RemindMeDate != nil || got.Flagged != nil || got.RecurrenceRules != nil || got.ClearDueDate {
		t.Errorf("update touched unsynced fields: %+v", got)
	}

	if _, err := s.Update(ctx, "missing", engine.Change{Title: ptr("x")}); err == nil {
		t.Error("want an error updating a missing reminder")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient()
	r := c.add(listID, "Milk", false)
	s := NewStore(c, listID)

	if err := s.Delete(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, r.ID); err != nil {
		t.Errorf("deleting it again: %v, want nil", err)
	}
	if err := s.Delete(ctx, "LOCKED"); err == nil {
		t.Error("want the per-reminder error")
	}
}

func TestDueDates(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient()
	s := NewStore(c, listID)
	local := func(y int, m time.Month, d, h, mi int) time.Time {
		return time.Date(y, m, d, h, mi, 0, 0, time.Local)
	}

	// Created with a date: midnight local.
	it, err := s.Create(ctx, engine.Item{Title: "Pay rent", Due: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if want := local(2026, 11, 1, 0, 0); c.creates[0].DueDate == nil || !c.creates[0].DueDate.Equal(want) || it.Due != "2026-11-01" {
		t.Errorf("created with %v, returned %+v", c.creates[0].DueDate, it)
	}

	// Moving a reminder that has a time keeps the time.
	timed := c.add(listID, "Call the vet", false)
	c.reminders[len(c.reminders)-1].DueDate = ptr(local(2026, 11, 2, 9, 30).UTC())
	it, err = s.Update(ctx, timed.ID, engine.Change{Due: ptr("2026-11-04")})
	if err != nil {
		t.Fatal(err)
	}
	if want := local(2026, 11, 4, 9, 30); !c.updates[timed.ID].DueDate.Equal(want) || it.Due != "2026-11-04" {
		t.Errorf("sent %v, returned %+v; want %v", c.updates[timed.ID].DueDate, it, want)
	}

	// A reminder with no date gets midnight.
	undated := c.add(listID, "Haircut", false)
	if _, err := s.Update(ctx, undated.ID, engine.Change{Due: ptr("2026-11-05")}); err != nil {
		t.Fatal(err)
	}
	if want := local(2026, 11, 5, 0, 0); !c.updates[undated.ID].DueDate.Equal(want) {
		t.Errorf("sent %v, want %v", c.updates[undated.ID].DueDate, want)
	}

	// An empty date clears it.
	it, err = s.Update(ctx, undated.ID, engine.Change{Due: ptr("")})
	if err != nil || !c.updates[undated.ID].ClearDueDate || it.Due != "" {
		t.Errorf("clear: sent %+v, returned %+v, %v", c.updates[undated.ID], it, err)
	}

	// Items reports the local date.
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if items[1].Due != "2026-11-04" {
		t.Errorf("items = %+v", items)
	}
}

func TestDueDateErrors(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient()
	s := NewStore(c, listID)
	r := c.add(listID, "Milk", false)

	if _, err := s.Create(ctx, engine.Item{Title: "Milk", Due: "soon"}); err == nil {
		t.Error("create with a bad date: want an error")
	}
	if _, err := s.Update(ctx, r.ID, engine.Change{Due: ptr("soon")}); err == nil {
		t.Error("update with a bad date: want an error")
	}
	if _, err := s.Update(ctx, "missing", engine.Change{Due: ptr("2026-11-01")}); !errors.Is(err, eventkit.ErrNotFound) {
		t.Errorf("update of a missing reminder: err = %v", err)
	}
	if len(c.creates) != 0 || len(c.updates) != 0 {
		t.Errorf("wrote anyway: creates %v, updates %v", c.creates, c.updates)
	}
}
