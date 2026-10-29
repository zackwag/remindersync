// Package reminders is the hub side of a pair: an [engine.Store] over one
// Apple Reminders list, read and written through go-eventkit.
package reminders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/engine"
)

var (
	// ErrListNotFound means the pair's list ID isn't in EventKit any more,
	// for example after an iCloud reset. The pair has to be pointed at the
	// list again; there's deliberately no fallback to matching by title.
	ErrListNotFound = errors.New("reminders: list not found")

	// ErrAmbiguousList means another list has the same name as the pair's
	// list. go-eventkit can only create a reminder by list name, so creating
	// one could land in the wrong list.
	ErrAmbiguousList = errors.New("reminders: another list has the same name")
)

// Client is the part of go-eventkit's *reminders.Client the store uses.
type Client interface {
	Lists() ([]eventkit.List, error)
	Reminders(opts ...eventkit.ListOption) ([]eventkit.Reminder, error)
	Reminder(id string) (*eventkit.Reminder, error)
	CreateReminder(input eventkit.CreateReminderInput) (*eventkit.Reminder, error)
	UpdateReminder(id string, input eventkit.UpdateReminderInput) (*eventkit.Reminder, error)
	DeleteReminders(ids []string) map[string]error
}

// Store is one Reminders list, identified by its EventKit list ID.
type Store struct {
	client Client
	listID string
}

var _ engine.Store = (*Store)(nil)

// NewStore returns a store for the list with the given EventKit ID.
func NewStore(client Client, listID string) *Store {
	return &Store{client: client, listID: listID}
}

// Items returns every reminder in the list, completed ones included. It
// fails with [ErrListNotFound] if the list is gone, rather than reporting an
// empty list.
func (s *Store) Items(ctx context.Context) ([]engine.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, _, err := s.list(); err != nil {
		return nil, err
	}
	// go-eventkit v0.16.0's WithListID matches by name, not ID, so fetch
	// everything and filter here. Reading all ~900 of a typical account's
	// reminders takes about 130 ms.
	all, err := s.client.Reminders()
	if err != nil {
		return nil, err
	}
	var items []engine.Item
	for _, r := range all {
		if r.ListID == s.listID {
			items = append(items, toItem(r))
		}
	}
	return items, nil
}

// Create adds an incomplete reminder to the list, due on from.Due if it has
// one.
func (s *Store) Create(ctx context.Context, from engine.Item) (engine.Item, error) {
	if err := ctx.Err(); err != nil {
		return engine.Item{}, err
	}
	list, all, err := s.list()
	if err != nil {
		return engine.Item{}, err
	}
	// go-eventkit resolves ListName case-insensitively, so a name shared with
	// any other list could send the reminder there instead.
	for _, other := range all {
		if other.ID != list.ID && strings.EqualFold(other.Title, list.Title) {
			return engine.Item{}, fmt.Errorf("%w: %q", ErrAmbiguousList, list.Title)
		}
	}

	input := eventkit.CreateReminderInput{Title: from.Title, ListName: list.Title}
	if from.Due != "" {
		due, err := dueTime(from.Due, nil)
		if err != nil {
			return engine.Item{}, err
		}
		input.DueDate = &due
	}
	r, err := s.client.CreateReminder(input)
	if err != nil {
		return engine.Item{}, err
	}
	if r.ListID != s.listID {
		// The list was renamed between the lookup and the create, or the
		// name check missed something. Don't leave a stray reminder behind.
		cleanup := s.client.DeleteReminders([]string{r.ID})[r.ID]
		return engine.Item{}, errors.Join(
			fmt.Errorf("reminders: %q was created in list %q, not %q", from.Title, r.List, list.Title),
			cleanup)
	}
	return toItem(*r), nil
}

// Update writes the non-nil fields of change. Fields the engine doesn't sync,
// such as notes and priority, are never touched. A new due date keeps the
// reminder's time of day, if it has one.
func (s *Store) Update(ctx context.Context, id string, change engine.Change) (engine.Item, error) {
	if err := ctx.Err(); err != nil {
		return engine.Item{}, err
	}
	input := eventkit.UpdateReminderInput{Title: change.Title, Completed: change.Completed}
	switch {
	case change.Due == nil:
	case *change.Due == "":
		input.ClearDueDate = true
	default:
		cur, err := s.client.Reminder(id)
		if err != nil {
			return engine.Item{}, err
		}
		due, err := dueTime(*change.Due, cur.DueDate)
		if err != nil {
			return engine.Item{}, err
		}
		input.DueDate = &due
	}
	r, err := s.client.UpdateReminder(id, input)
	if err != nil {
		return engine.Item{}, err
	}
	return toItem(*r), nil
}

// Delete removes a reminder. A reminder that's already gone isn't an error.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The bulk delete skips missing reminders, where the single one fails.
	return s.client.DeleteReminders([]string{id})[id]
}

// list returns the pair's list and every list, so callers can check names.
func (s *Store) list() (eventkit.List, []eventkit.List, error) {
	all, err := s.client.Lists()
	if err != nil {
		return eventkit.List{}, nil, err
	}
	for _, l := range all {
		if l.ID == s.listID {
			return l, all, nil
		}
	}
	return eventkit.List{}, nil, fmt.Errorf("%w: %s", ErrListNotFound, s.listID)
}

func toItem(r eventkit.Reminder) engine.Item {
	it := engine.Item{ID: r.ID, Title: r.Title, Completed: r.Completed}
	if r.DueDate != nil {
		it.Due = r.DueDate.Local().Format(engine.DateLayout)
	}
	if r.CreatedAt != nil {
		it.CreatedAt = *r.CreatedAt
	}
	return it
}

// dueTime is midnight local time on date, or the time of day of cur if the
// reminder already has one. go-eventkit always writes a time with a due date,
// so a reminder that had only a date gets midnight.
func dueTime(date string, cur *time.Time) (time.Time, error) {
	d, err := time.ParseInLocation(engine.DateLayout, date, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("reminders: due date: %w", err)
	}
	if cur != nil {
		c := cur.Local()
		d = time.Date(d.Year(), d.Month(), d.Day(), c.Hour(), c.Minute(), c.Second(), 0, time.Local)
	}
	return d, nil
}
