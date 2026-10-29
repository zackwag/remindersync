package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/engine"
)

func pair(name, targetList string) Pair {
	return Pair{
		ID:        "id-" + name,
		Name:      name,
		Direction: engine.TwoWay,
		CreatedAt: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC),
		Reminders: RemindersList{ListID: "B6A189C2", ListTitle: "Groceries"},
		Target:    Target{Type: TargetSkylight, FrameID: "5760005", ListID: targetList, Label: "Grocery List"},
	}
}

func chorePair(name, categoryID string) Pair {
	p := pair(name, "")
	p.Reminders = RemindersList{ListID: "8BCC4FE2", ListTitle: "Personal Reminders"}
	p.Target = Target{Type: TargetSkylightChores, FrameID: "5760005", CategoryID: categoryID, Label: "Zack"}
	return p
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Config{Pairs: []Pair{pair("groceries", "7763026"), pair("todo", "7763027"), chorePair("chores", "24197547")}}
	want.Pairs[1].Direction = engine.OneWay
	want.Pairs[2].Target.DaysAhead = new(3)

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode = %v, want no group or other access", info.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestSaveWritesTheDesignedLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, Config{Pairs: []Pair{pair("groceries", "7763026")}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[[pair]]", `name = "groceries"`, `direction = "two-way"`, "[pair.reminders]", "[pair.target]", `type = "skylight"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config.toml is missing %s:\n%s", want, b)
		}
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(cfg.Pairs) != 0 {
		t.Errorf("got %+v, %v; want an empty config", cfg, err)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"bad toml":    "[[pair]\n",
		"unknown key": "[[pair]]\nid = \"a\"\nnickname = \"unknown\"\n",
		"invalid":     "[[pair]]\nid = \"a\"\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestSaveRefusesInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	bad := pair("groceries", "7763026")
	bad.Direction = "sideways"
	if err := Save(path, Config{Pairs: []Pair{bad}}); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an invalid config was written: %v", err)
	}
}

func TestSaveErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(file, "config.toml"), Config{}); err == nil {
		t.Error("want an error when the directory can't be created")
	}

	dir := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, Config{}); err == nil {
		t.Error("want an error when the path is a directory")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"valid", func(*Config) {}, ""},
		{"missing fields", func(c *Config) { c.Pairs[0].ID, c.Pairs[0].Reminders.ListID = "", "" }, "missing id, reminders.list_id"},
		{"bad direction", func(c *Config) { c.Pairs[0].Direction = "both" }, "direction must be"},
		{"unknown target", func(c *Config) { c.Pairs[0].Target.Type = "trello" }, `unknown target type "trello"`},
		{"skylight without list", func(c *Config) { c.Pairs[0].Target.ListID = "" }, "needs frame_id and list_id"},
		{"duplicate id", func(c *Config) { c.Pairs[1].ID = c.Pairs[0].ID }, "used twice"},
		{"duplicate name", func(c *Config) { c.Pairs[1].Name = c.Pairs[0].Name }, `"groceries" is used twice`},
		{"two pairs on one target", func(c *Config) { c.Pairs[1].Target.ListID = c.Pairs[0].Target.ListID }, "both sync to"},
		{"two pairs on one Reminders list is fine", func(c *Config) { c.Pairs[1].Reminders = c.Pairs[0].Reminders }, ""},
		{"chores without category", func(c *Config) { c.Pairs[2].Target.CategoryID = "" }, "needs frame_id and category_id"},
		{"two pairs on one category", func(c *Config) { c.Pairs = append(c.Pairs, chorePair("more", "24197547")) }, "both sync to Skylight chores for Zack"},
		{"two categories is fine", func(c *Config) { c.Pairs = append(c.Pairs, chorePair("amelia", "24197710")) }, ""},
		{"negative days ahead", func(c *Config) { c.Pairs[0].Target.DaysAhead = new(-1) }, "days_ahead must be from 0 to 365, got -1"},
		{"too many days ahead", func(c *Config) { c.Pairs[2].Target.DaysAhead = new(366) }, "got 366"},
		{"zero days ahead is fine", func(c *Config) { c.Pairs[0].Target.DaysAhead = new(0) }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Pairs: []Pair{pair("groceries", "7763026"), pair("todo", "7763027"), chorePair("chores", "24197547")}}
			tt.mutate(&c)
			err := c.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestPairString(t *testing.T) {
	p := pair("groceries", "7763026")
	if got, want := p.String(), `groceries: Reminders "Groceries" <-> Skylight "Grocery List"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	p.Direction = engine.OneWay
	p.Target.Type = "trello"
	if got, want := p.String(), `groceries: Reminders "Groceries" -> trello`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	c := chorePair("chores", "24197547")
	if got, want := c.String(), `chores: Reminders "Personal Reminders" <-> Skylight chores for Zack`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if !c.Target.Dates() || p.Target.Dates() {
		t.Error("only chores keep dates")
	}
}

func TestTargetAhead(t *testing.T) {
	if got := pair("groceries", "7763026").Target.Ahead(); got != DefaultListDaysAhead {
		t.Errorf("list default = %d", got)
	}
	c := chorePair("chores", "24197547")
	if got := c.Target.Ahead(); got != DefaultChoreDaysAhead {
		t.Errorf("chore default = %d", got)
	}
	c.Target.DaysAhead = new(0)
	if got := c.Target.Ahead(); got != 0 {
		t.Errorf("set to 0, got %d", got)
	}
}

func TestFind(t *testing.T) {
	c := Config{Pairs: []Pair{pair("groceries", "1")}}
	if p, ok := c.Find("groceries"); !ok || p.ID != "id-groceries" {
		t.Errorf("Find(groceries) = %+v, %t", p, ok)
	}
	if _, ok := c.Find("todo"); ok {
		t.Error("Find(todo) found something")
	}
}

func TestNewID(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := NewID(), NewID()
	if !uuid.MatchString(a) || a == b {
		t.Errorf("NewID() = %s, %s", a, b)
	}
}

func TestDefaultPath(t *testing.T) {
	p, err := DefaultPath()
	if err != nil || !strings.HasSuffix(p, filepath.Join("remindersync", "config.toml")) {
		t.Errorf("DefaultPath() = %s, %v", p, err)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := DefaultPath(); err == nil {
		t.Error("want an error with no home directory")
	}
}
