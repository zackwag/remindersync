package skylight

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zackwag/remindersync/internal/engine"
	api "github.com/zackwag/remindersync/skylight"
)

const (
	// LookbackDays is how far back a chore read goes. Completed chores are
	// listed under the day they were checked off, so this is how long a
	// checked-off chore stays visible to the sync. Past it, a linked chore
	// looks deleted, which for a completed reminder only drops the link.
	LookbackDays = 30

	// AheadDays is how far forward a chore read goes: the engine's horizon,
	// plus a month for a chore moved past it on the frame.
	AheadDays = engine.HorizonDays + 30
)

// ChoreClient is the part of *skylight.Client the chore store uses.
type ChoreClient interface {
	Chores(ctx context.Context, frameID string, q api.ChoreQuery) ([]api.Chore, error)
	AddChore(ctx context.Context, frameID string, chore api.NewChore) (api.Chore, error)
	UpdateChore(ctx context.Context, frameID, choreID string, update api.ChoreUpdate) (api.Chore, error)
	DeleteChore(ctx context.Context, frameID, choreID string) error
}

// ChoreStore is one category's chores on a Skylight frame: one-off chores,
// each due on a date. Repeating and up-for-grabs chores are left out, since
// the sync didn't make them and can't map them to a reminder.
type ChoreStore struct {
	client     ChoreClient
	frameID    string
	categoryID string
	now        func() time.Time
}

var _ engine.Store = (*ChoreStore)(nil)

// NewChoreStore returns a store for a category's chores. now defaults to
// time.Now.
func NewChoreStore(client ChoreClient, frameID, categoryID string, now func() time.Time) *ChoreStore {
	if now == nil {
		now = time.Now
	}
	return &ChoreStore{client: client, frameID: frameID, categoryID: categoryID, now: now}
}

// Items returns the category's one-off chores from [LookbackDays] ago to
// [AheadDays] ahead, plus overdue ones.
func (s *ChoreStore) Items(ctx context.Context) ([]engine.Item, error) {
	today := s.now().Local()
	chores, err := s.client.Chores(ctx, s.frameID, api.ChoreQuery{
		After:       today.AddDate(0, 0, -LookbackDays).Format(engine.DateLayout),
		Before:      today.AddDate(0, 0, AheadDays).Format(engine.DateLayout),
		CategoryID:  s.categoryID,
		IncludeLate: true,
	})
	if err != nil {
		return nil, err
	}
	if len(chores) >= MaxItems {
		return nil, fmt.Errorf("%w: got %d chores", ErrTruncated, len(chores))
	}
	items := make([]engine.Item, 0, len(chores))
	for _, ch := range chores {
		if ch.Recurring || ch.UpForGrabs || ch.CategoryID != s.categoryID {
			continue
		}
		items = append(items, choreItem(ch))
	}
	return items, nil
}

// Create adds a pending chore due on from.Due, or today if it has none.
func (s *ChoreStore) Create(ctx context.Context, from engine.Item) (engine.Item, error) {
	start := from.Due
	if start == "" {
		start = s.now().Local().Format(engine.DateLayout)
	}
	ch, err := s.client.AddChore(ctx, s.frameID, api.NewChore{Summary: from.Title, Start: start, CategoryID: s.categoryID})
	if err != nil {
		return engine.Item{}, err
	}
	return choreItem(ch), nil
}

// Update writes the non-nil fields of change. A chore always has a date, so
// an empty Due is ignored. Skylight won't change a chore's status and its
// other fields in one request, so a change to both is sent as two, with the
// other fields changed while the chore is open: a reopen goes first and a
// completion last. A chore that's moved while completed and then reopened
// says pending in the response but stays completed in every later list.
func (s *ChoreStore) Update(ctx context.Context, id string, change engine.Change) (engine.Item, error) {
	fields := api.ChoreUpdate{Summary: change.Title}
	if change.Due != nil && *change.Due != "" {
		fields.Start = change.Due
	}
	var upds []api.ChoreUpdate
	if fields.Summary != nil || fields.Start != nil || change.Completed == nil {
		upds = append(upds, fields)
	}
	if change.Completed != nil {
		status := api.ChorePending
		if *change.Completed {
			status = api.ChoreComplete
		}
		upd := api.ChoreUpdate{Status: &status}
		if *change.Completed {
			upds = append(upds, upd)
		} else {
			upds = append([]api.ChoreUpdate{upd}, upds...)
		}
	}
	var ch api.Chore
	for _, upd := range upds {
		var err error
		if ch, err = s.client.UpdateChore(ctx, s.frameID, id, upd); err != nil {
			return engine.Item{}, err
		}
	}
	return choreItem(ch), nil
}

// Delete removes a chore. One that's already gone isn't an error.
func (s *ChoreStore) Delete(ctx context.Context, id string) error {
	err := s.client.DeleteChore(ctx, s.frameID, id)
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// choreItem converts a chore. Chores have no created_at, so an interrupted
// create of one can't be recovered and may leave a duplicate.
func choreItem(ch api.Chore) engine.Item {
	return engine.Item{ID: ch.ID, Title: ch.Summary, Completed: ch.Status == api.ChoreComplete, Due: ch.Start}
}
