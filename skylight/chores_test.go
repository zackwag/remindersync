package skylight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// choreJSON is a chore as Skylight returns it, trimmed to what's read.
const choreJSON = `{"id":"114549764","type":"chore","attributes":{"id":114549764,"summary":"Pay rent","status":"complete","start":"2030-01-08","completed_on":"2026-10-28","recurring":false,"up_for_grabs":false},"relationships":{"category":{"data":{"id":"24197547","type":"category"}},"habit_tracker":{"data":null}}}`

var wantChore = Chore{ID: "114549764", Summary: "Pay rent", Status: ChoreComplete, Start: "2030-01-08", CategoryID: "24197547", CompletedOn: "2026-10-28"}

func TestCategories(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/frames/f1/categories" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"24197547","type":"category_detail","attributes":{"label":"Zack","selected_for_chore_chart":true}},{"id":"9","type":"category_detail","attributes":{"label":"Family"}}]}`)
	})
	got, err := c.Categories(context.Background(), "f1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Category{{ID: "24197547", Label: "Zack", ChoreChart: true}, {ID: "9", Label: "Family"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestChores(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/frames/f1/chores" || q.Get("after") != "2026-10-01" || q.Get("before") != "2027-12-01" ||
			q.Get("category_id") != "24197547" || q.Get("include_late") != "true" {
			t.Errorf("%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"data":[`+choreJSON+`,{"id":"2","type":"chore","attributes":{"summary":"Up for grabs","status":"pending","start":"2026-11-01","recurring":true,"up_for_grabs":true,"completed_on":null},"relationships":{"category":{"data":null}}}],"included":[]}`)
	})
	got, err := c.Chores(context.Background(), "f1", ChoreQuery{After: "2026-10-01", Before: "2027-12-01", CategoryID: "24197547", IncludeLate: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []Chore{wantChore, {ID: "2", Summary: "Up for grabs", Status: ChorePending, Start: "2026-11-01", Recurring: true, UpForGrabs: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestChoresQueryOmitsUnsetFilters(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Has("category_id") || q.Has("include_late") {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"data":[],"included":[]}`)
	})
	got, err := c.Chores(context.Background(), "f1", ChoreQuery{After: "2026-10-01", Before: "2026-10-31"})
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestAddChore(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/frames/f1/chores" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if want := map[string]string{"summary": "Pay rent", "start": "2030-01-08", "category_id": "24197547"}; !reflect.DeepEqual(got, want) {
			t.Errorf("body = %v", got)
		}
		_, _ = io.WriteString(w, `{"data":`+choreJSON+`,"included":[]}`)
	})
	got, err := c.AddChore(context.Background(), "f1", NewChore{Summary: "Pay rent", Start: "2030-01-08", CategoryID: "24197547"})
	if err != nil || got != wantChore {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestUpdateChoreSendsOnlySetFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/frames/f1/chores/114549764" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"start":"2030-01-08","status":"pending"}` {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"data":`+choreJSON+`}`)
	})
	start, status := "2030-01-08", ChorePending
	got, err := c.UpdateChore(context.Background(), "f1", "114549764", ChoreUpdate{Start: &start, Status: &status})
	if err != nil || got != wantChore {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestChoreResponses(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		wantErr    error
	}{
		{"empty", ``, ErrChoreNotReturned},
		{"null", `{"data":null}`, ErrChoreNotReturned},
		{"other type", `{"data":{"id":"1","type":"category"}}`, ErrChoreNotReturned},
		{"not json", `<html>`, nil},
		{"bad chore", `{"data":{"id":"1","type":"chore","attributes":{"summary":7}}}`, nil},
		{"bad data", `{"data":{"id":[]}}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.AddChore(context.Background(), "f1", NewChore{})
			if err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestChoreErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/frames/f1/chores":
			_, _ = io.WriteString(w, `{"data":[{"id":"1","type":"chore","attributes":{"status":[]}}]}`)
		case "/api/frames/f1/categories":
			_, _ = io.WriteString(w, `{"data":[{"id":"1","type":"category","attributes":{"label":[]}}]}`)
		case "/api/frames/f2/chores", "/api/frames/f2/categories":
			_, _ = io.WriteString(w, `{"data":[{"id":[]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	for name, err := range map[string]error{
		"Chores bad attributes":     second(c.Chores(ctx, "f1", ChoreQuery{})),
		"Chores bad data":           second(c.Chores(ctx, "f2", ChoreQuery{})),
		"Chores 404":                second(c.Chores(ctx, "f3", ChoreQuery{})),
		"Categories bad attributes": second(c.Categories(ctx, "f1")),
		"Categories bad data":       second(c.Categories(ctx, "f2")),
		"Categories 404":            second(c.Categories(ctx, "f3")),
		"UpdateChore 404":           second(c.UpdateChore(ctx, "f3", "1", ChoreUpdate{})),
		"AddChore 404":              second(c.AddChore(ctx, "f3", NewChore{})),
	} {
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	var apiErr *APIError
	if err := c.DeleteChore(ctx, "f3", "1"); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("DeleteChore of a missing chore: err = %v, want a 404 APIError", err)
	}
}

func TestDeleteChore(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/frames/f1/chores/9" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
	})
	if err := c.DeleteChore(context.Background(), "f1", "9"); err != nil {
		t.Fatal(err)
	}
}

func second[T any](_ T, err error) error { return err }
