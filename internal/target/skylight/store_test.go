package skylight

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/engine"
	api "github.com/zackwag/remindersync/skylight"
)

const (
	frameID = "5760005"
	listID  = "7763027"
)

type fakeClient struct {
	items []api.ListItem
	next  int

	itemsErr, addErr, updateErr, deleteErr error

	updates []api.ListItemUpdate
}

func (f *fakeClient) add(label string, status api.ItemStatus) api.ListItem {
	f.next++
	li := api.ListItem{ID: fmt.Sprint(f.next), Label: label, Status: status, CreatedAt: "2026-10-28T13:11:04.198Z"}
	f.items = append(f.items, li)
	return li
}

func (f *fakeClient) check(frame, list string) error {
	if frame != frameID || list != listID {
		return fmt.Errorf("wrong frame or list: %s/%s", frame, list)
	}
	return nil
}

func (f *fakeClient) ListItems(_ context.Context, frame, list string) ([]api.ListItem, error) {
	if err := f.check(frame, list); err != nil {
		return nil, err
	}
	return slices.Clone(f.items), f.itemsErr
}

func (f *fakeClient) AddListItem(_ context.Context, frame, list, label string) (api.ListItem, error) {
	if err := f.check(frame, list); err != nil {
		return api.ListItem{}, err
	}
	if f.addErr != nil {
		return api.ListItem{}, f.addErr
	}
	return f.add(label, api.StatusPending), nil
}

func (f *fakeClient) UpdateListItem(_ context.Context, frame, list, id string, upd api.ListItemUpdate) (api.ListItem, error) {
	if err := f.check(frame, list); err != nil {
		return api.ListItem{}, err
	}
	f.updates = append(f.updates, upd)
	if f.updateErr != nil {
		return api.ListItem{}, f.updateErr
	}
	i := slices.IndexFunc(f.items, func(li api.ListItem) bool { return li.ID == id })
	if upd.Label != nil {
		f.items[i].Label = *upd.Label
	}
	if upd.Status != nil {
		f.items[i].Status = *upd.Status
	}
	return f.items[i], nil
}

func (f *fakeClient) DeleteListItem(_ context.Context, frame, list, id string) error {
	if err := f.check(frame, list); err != nil {
		return err
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.items = slices.DeleteFunc(f.items, func(li api.ListItem) bool { return li.ID == id })
	return nil
}

func ptr[T any](v T) *T { return &v }

var createdAt = time.Date(2026, 10, 28, 13, 11, 4, 198_000_000, time.UTC)

func TestItems(t *testing.T) {
	ctx := context.Background()
	c := &fakeClient{}
	c.add("Milk", api.StatusPending)
	c.add("Eggs", api.StatusCompleted)
	c.items = append(c.items, api.ListItem{ID: "9", Label: "No date", Status: api.StatusPending})

	items, err := NewStore(c, frameID, listID).Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.Item{
		{ID: "1", Title: "Milk", CreatedAt: createdAt},
		{ID: "2", Title: "Eggs", Completed: true, CreatedAt: createdAt},
		{ID: "9", Title: "No date"},
	}
	if !slices.Equal(items, want) {
		t.Errorf("got  %+v\nwant %+v", items, want)
	}
}

func TestItemsErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("client", func(t *testing.T) {
		boom := &api.APIError{StatusCode: 404}
		c := &fakeClient{itemsErr: boom}
		if _, err := NewStore(c, frameID, listID).Items(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a read at the cap may be truncated", func(t *testing.T) {
		c := &fakeClient{}
		for range MaxItems {
			c.add("x", api.StatusPending)
		}
		if _, err := NewStore(c, frameID, listID).Items(ctx); !errors.Is(err, ErrTruncated) {
			t.Errorf("err = %v, want ErrTruncated", err)
		}
	})

	t.Run("bad created_at", func(t *testing.T) {
		c := &fakeClient{}
		c.add("Milk", api.StatusPending)
		c.items[0].CreatedAt = "yesterday"
		if _, err := NewStore(c, frameID, listID).Items(ctx); err == nil {
			t.Error("want an error")
		}
	})
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	c := &fakeClient{}
	it, err := NewStore(c, frameID, listID).Create(ctx, "Milk")
	if err != nil {
		t.Fatal(err)
	}
	if it != (engine.Item{ID: "1", Title: "Milk", CreatedAt: createdAt}) {
		t.Errorf("got %+v", it)
	}

	c.addErr = errors.New("422")
	if _, err := NewStore(c, frameID, listID).Create(ctx, "Eggs"); !errors.Is(err, c.addErr) {
		t.Errorf("err = %v", err)
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	c := &fakeClient{}
	li := c.add("Milk", api.StatusPending)
	s := NewStore(c, frameID, listID)

	tests := []struct {
		name   string
		change engine.Change
		want   api.ListItemUpdate
		item   engine.Item
	}{
		{
			name:   "complete",
			change: engine.Change{Completed: ptr(true)},
			want:   api.ListItemUpdate{Status: ptr(api.StatusCompleted)},
			item:   engine.Item{ID: li.ID, Title: "Milk", Completed: true, CreatedAt: createdAt},
		},
		{
			name:   "reopen and rename",
			change: engine.Change{Title: ptr("Oat milk"), Completed: ptr(false)},
			want:   api.ListItemUpdate{Label: ptr("Oat milk"), Status: ptr(api.StatusPending)},
			item:   engine.Item{ID: li.ID, Title: "Oat milk", CreatedAt: createdAt},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Update(ctx, li.ID, tt.change)
			if err != nil {
				t.Fatal(err)
			}
			sent := c.updates[len(c.updates)-1]
			if fmt.Sprint(deref(sent.Label), deref(sent.Status)) != fmt.Sprint(deref(tt.want.Label), deref(tt.want.Status)) {
				t.Errorf("sent label=%v status=%v, want label=%v status=%v",
					deref(sent.Label), deref(sent.Status), deref(tt.want.Label), deref(tt.want.Status))
			}
			if got != tt.item {
				t.Errorf("returned %+v, want %+v", got, tt.item)
			}
		})
	}

	c.updateErr = errors.New("500")
	if _, err := s.Update(ctx, li.ID, engine.Change{Completed: ptr(true)}); !errors.Is(err, c.updateErr) {
		t.Errorf("err = %v", err)
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	c := &fakeClient{}
	li := c.add("Milk", api.StatusPending)
	s := NewStore(c, frameID, listID)

	if err := s.Delete(ctx, li.ID); err != nil {
		t.Fatal(err)
	}

	c.deleteErr = &api.APIError{StatusCode: 404}
	if err := s.Delete(ctx, li.ID); err != nil {
		t.Errorf("deleting a missing item: %v, want nil", err)
	}

	c.deleteErr = &api.APIError{StatusCode: 500}
	if err := s.Delete(ctx, li.ID); !errors.Is(err, c.deleteErr) {
		t.Errorf("err = %v, want the 500", err)
	}
}
