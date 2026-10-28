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

	listsErr, remindersErr, createErr, updateErr error
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
		if _, err := s.Create(canceled, "x"); !errors.Is(err, context.Canceled) {
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
		it, err := NewStore(c, listID).Create(ctx, "Milk")
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
		_, err := NewStore(c, listID).Create(ctx, "Milk")
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
		_, err := NewStore(c, listID).Create(ctx, "Milk")
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
		if _, err := NewStore(c, listID).Create(ctx, "Milk"); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		c = newFakeClient()
		c.lists = nil
		if _, err := NewStore(c, listID).Create(ctx, "Milk"); !errors.Is(err, ErrListNotFound) {
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
