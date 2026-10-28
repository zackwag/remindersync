package skylight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	opts = append([]Option{WithBaseURL(srv.URL), WithHTTPClient(srv.Client())}, opts...)
	return NewClient(Token{AccessToken: "access-1", RefreshToken: "refresh-1"}, opts...)
}

func TestRequestHeaders(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Skylight-Api-Version"); got != apiVersion {
			t.Errorf("Skylight-Api-Version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	if _, err := c.Frames(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshOn401(t *testing.T) {
	var apiCalls, tokenCalls atomic.Int32
	var refreshed Token
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.PostForm.Get("refresh_token"); got != "refresh-1" {
				t.Errorf("refresh_token = %q", got)
			}
			if got := r.PostForm.Get("skylight_api_client_device_fingerprint"); got != "fp" {
				t.Errorf("fingerprint = %q", got)
			}
			_, _ = io.WriteString(w, `{"access_token":"access-2","refresh_token":"refresh-2"}`)
			return
		}
		apiCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer access-2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}, WithDeviceFingerprint("fp"), WithTokenRefreshHook(func(t Token) { refreshed = t }))

	if _, err := c.Frames(context.Background()); err != nil {
		t.Fatal(err)
	}
	if apiCalls.Load() != 2 || tokenCalls.Load() != 1 {
		t.Errorf("api calls = %d, token calls = %d; want 2 and 1", apiCalls.Load(), tokenCalls.Load())
	}
	want := Token{AccessToken: "access-2", RefreshToken: "refresh-2"}
	if refreshed != want || c.Token() != want {
		t.Errorf("hook got %+v, client has %+v; want %+v", refreshed, c.Token(), want)
	}
}

func TestStillUnauthorizedAfterRefresh(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_, _ = io.WriteString(w, `{"access_token":"access-2"}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Frames(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if got := c.Token().RefreshToken; got != "refresh-1" {
		t.Errorf("refresh token = %q, want the old one kept when not rotated", got)
	}
}

func TestRefreshRejected(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Frames(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestAPIErrorEchoesPayloadOn422(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"errors":["label can't be blank"]}`)
	})
	_, err := c.AddListItem(context.Background(), "f", "l", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Body, `sent {"label":""}`) {
		t.Errorf("unexpected error: %v", apiErr)
	}
}

func TestFramesPrefersHouseholdName(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[
			{"id":"1","type":"frame","attributes":{"name":"byrd-malone-7772","household_name":"Byrd & Malone"}},
			{"id":2,"type":"frame","attributes":{"name":"slug-only"}}
		]}`)
	})
	frames, err := c.Frames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Frame{{ID: "1", Name: "Byrd & Malone"}, {ID: "2", Name: "slug-only"}}
	if len(frames) != 2 || frames[0] != want[0] || frames[1] != want[1] {
		t.Errorf("frames = %+v, want %+v", frames, want)
	}
}

func TestListItemsSortedFromIncluded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/frames/f1/lists/l1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{
			"data":{"id":"l1","type":"list","attributes":{"label":"To-Do"}},
			"included":[
				{"id":"b","type":"list_item","attributes":{"label":"Second","status":"completed","position":2}},
				{"id":"x","type":"category","attributes":{}},
				{"id":"a","type":"list_item","attributes":{"label":"First","position":1,"updated_at":"2026-10-27T19:00:00Z"}}
			]}`)
	})
	items, err := c.ListItems(context.Background(), "f1", "l1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "a" || items[0].Status != StatusPending || items[0].UpdatedAt != "2026-10-27T19:00:00Z" {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[1].ID != "b" || items[1].Status != StatusCompleted {
		t.Errorf("items[1] = %+v", items[1])
	}
}

func TestAddListItem(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantID  string
		wantErr error
	}{
		{"jsonapi", `{"data":{"id":"99","type":"list_item","attributes":{"label":"Milk","status":"pending"}}}`, "99", nil},
		{"included", `{"data":{"id":"l1","type":"list"},"included":[{"id":"99","type":"list_item","attributes":{"label":"Milk"}}]}`, "99", nil},
		{"flat", `{"id":99,"label":"Milk","status":"pending"}`, "99", nil},
		{"empty", ``, "", ErrItemNotReturned},
		{"other", `{"data":{"id":"l1","type":"list"}}`, "", ErrItemNotReturned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/frames/f1/lists/l1/list_items" {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				var got map[string]string
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got["label"] != "Milk" {
					t.Errorf("body = %v, err = %v", got, err)
				}
				_, _ = io.WriteString(w, tt.body)
			})
			item, err := c.AddListItem(context.Background(), "f1", "l1", "Milk")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if item.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", item.ID, tt.wantID)
			}
		})
	}
}

func TestUpdateListItemSendsOnlySetFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/frames/f1/lists/l1/list_items/99" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"status":"completed"}` {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"data":{"id":"99","type":"list_item","attributes":{"label":"Milk","status":"completed"}}}`)
	})
	status := StatusCompleted
	item, err := c.UpdateListItem(context.Background(), "f1", "l1", "99", ListItemUpdate{Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusCompleted {
		t.Errorf("status = %q", item.Status)
	}
}

func TestDeleteListItem(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/frames/f1/lists/l1/list_items/99" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteListItem(context.Background(), "f1", "l1", "99"); err != nil {
		t.Fatal(err)
	}
}

func TestPKCE(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Verifier); n < 43 || n > 128 {
		t.Errorf("verifier length %d outside RFC 7636 bounds", n)
	}
	u, err := url.Parse(OAuth{}.AuthorizeURL(p.Challenge))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge") != p.Challenge || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != RedirectURI {
		t.Errorf("authorize URL query = %v", q)
	}
}

func TestExtractCode(t *testing.T) {
	tests := map[string]string{
		"abc123":      "abc123",
		"  abc123\n":  "abc123",
		"code=abc123": "abc123",
		"https://ourskylight.com/welcome?code=abc123": "abc123",
	}
	for in, want := range tests {
		got, err := ExtractCode(in)
		if err != nil || got != want {
			t.Errorf("ExtractCode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "https://ourskylight.com/welcome?state=x"} {
		if _, err := ExtractCode(bad); err == nil {
			t.Errorf("ExtractCode(%q) succeeded, want error", bad)
		}
	}
}

func TestExchangeCodeRequiresRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code_verifier") != "v" {
			t.Errorf("form = %v", r.PostForm)
		}
		_, _ = io.WriteString(w, `{"access_token":"a"}`)
	}))
	defer srv.Close()
	_, err := OAuth{BaseURL: srv.URL}.ExchangeCode(context.Background(), "c", "v")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}
