// Package skylight is a target for pairs: an [engine.Store] over one Skylight
// list.
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

// MaxItems is the size at which a list read is treated as truncated. Skylight's
// chores endpoint silently stops at 5,000 items. Lists haven't been seen to,
// but if they do, a cut-off read would make every item past the cut look
// deleted, so a read that reaches the cap fails instead.
const MaxItems = 5000

// ErrTruncated means a list read hit [MaxItems].
var ErrTruncated = errors.New("skylight: list read may be truncated")

// Client is the part of *skylight.Client the store uses.
type Client interface {
	ListItems(ctx context.Context, frameID, listID string) ([]api.ListItem, error)
	AddListItem(ctx context.Context, frameID, listID, label string) (api.ListItem, error)
	UpdateListItem(ctx context.Context, frameID, listID, itemID string, update api.ListItemUpdate) (api.ListItem, error)
	DeleteListItem(ctx context.Context, frameID, listID, itemID string) error
}

// Store is one list on one Skylight frame.
type Store struct {
	client  Client
	frameID string
	listID  string
}

var _ engine.Store = (*Store)(nil)

// NewStore returns a store for a list on a frame.
func NewStore(client Client, frameID, listID string) *Store {
	return &Store{client: client, frameID: frameID, listID: listID}
}

// Items returns every item on the list, completed ones included. A list
// that's gone is an error from Skylight, never an empty list.
func (s *Store) Items(ctx context.Context) ([]engine.Item, error) {
	listItems, err := s.client.ListItems(ctx, s.frameID, s.listID)
	if err != nil {
		return nil, err
	}
	if len(listItems) >= MaxItems {
		return nil, fmt.Errorf("%w: got %d items", ErrTruncated, len(listItems))
	}
	items := make([]engine.Item, 0, len(listItems))
	for _, li := range listItems {
		it, err := toItem(li)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

// Create adds a pending item to the list.
func (s *Store) Create(ctx context.Context, title string) (engine.Item, error) {
	li, err := s.client.AddListItem(ctx, s.frameID, s.listID, title)
	if err != nil {
		return engine.Item{}, err
	}
	return toItem(li)
}

// Update writes the non-nil fields of change.
func (s *Store) Update(ctx context.Context, id string, change engine.Change) (engine.Item, error) {
	var upd api.ListItemUpdate
	upd.Label = change.Title
	if change.Completed != nil {
		status := api.StatusPending
		if *change.Completed {
			status = api.StatusCompleted
		}
		upd.Status = &status
	}
	li, err := s.client.UpdateListItem(ctx, s.frameID, s.listID, id, upd)
	if err != nil {
		return engine.Item{}, err
	}
	return toItem(li)
}

// Delete removes an item. An item that's already gone isn't an error.
func (s *Store) Delete(ctx context.Context, id string) error {
	err := s.client.DeleteListItem(ctx, s.frameID, s.listID, id)
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func toItem(li api.ListItem) (engine.Item, error) {
	it := engine.Item{ID: li.ID, Title: li.Label, Completed: li.Status == api.StatusCompleted}
	if li.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, li.CreatedAt)
		if err != nil {
			return engine.Item{}, fmt.Errorf("skylight: item %s has created_at %q: %w", li.ID, li.CreatedAt, err)
		}
		it.CreatedAt = t
	}
	return it, nil
}
