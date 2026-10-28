package launchd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlist(t *testing.T) {
	a := Agent{
		Label:   Label,
		Program: []string{"/Users/me/Library/Application Support/remindersync/bin/remindersync", "daemon", "-poll", "1m0s"},
		LogPath: "/Users/me/Library/Logs/remindersync/R&D <daemon>.log",
	}
	got := string(a.Plist())
	for _, want := range []string{
		"<string>com.zackwag.remindersync</string>",
		"\t\t<string>/Users/me/Library/Application Support/remindersync/bin/remindersync</string>\n\t\t<string>daemon</string>\n\t\t<string>-poll</string>\n\t\t<string>1m0s</string>\n",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<string>Aqua</string>",
		"<string>/Users/me/Library/Logs/remindersync/R&amp;D &lt;daemon&gt;.log</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist is missing %q:\n%s", want, got)
		}
	}

	// plutil is the authority on whether it's a valid plist.
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("no plutil")
	}
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(path, a.Plist(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v: %s", err, out)
	}
}

// fake records launchctl calls and fails the ones named in fail.
type fake struct {
	calls []string
	fail  map[string]int
}

func (f *fake) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.fail[args[0]] > 0 {
		f.fail[args[0]]--
		return []byte(args[0] + " failed: 5: Input/output error\n"), errors.New("exit status 5")
	}
	return nil, nil
}

func TestLoad(t *testing.T) {
	bootstrapWait = time.Millisecond
	d := Domain()

	f := &fake{fail: map[string]int{"bootout": 1, "bootstrap": 2}}
	if err := Load(f.run, "/p.plist", "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{"bootout " + d + "/x", "bootstrap " + d + " /p.plist", "bootstrap " + d + " /p.plist", "bootstrap " + d + " /p.plist"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}

	f = &fake{fail: map[string]int{"bootstrap": bootstrapTries}}
	err := Load(f.run, "/p.plist", "x")
	if err == nil || !strings.Contains(err.Error(), "bootstrap failed: 5: Input/output error") {
		t.Errorf("err = %v", err)
	}
}

func TestUnload(t *testing.T) {
	f := &fake{fail: map[string]int{"print": 1}}
	if loaded, err := Unload(f.run, "x"); loaded || err != nil {
		t.Errorf("not loaded: %v, %v", loaded, err)
	}

	f = &fake{}
	if loaded, err := Unload(f.run, "x"); !loaded || err != nil {
		t.Errorf("loaded: %v, %v", loaded, err)
	}
	if want := []string{"print " + Domain() + "/x", "bootout " + Domain() + "/x"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q", f.calls)
	}

	f = &fake{fail: map[string]int{"bootout": 1}}
	if _, err := Unload(f.run, "x"); err == nil || !strings.Contains(err.Error(), "bootout failed") {
		t.Errorf("err = %v", err)
	}
}

func TestExec(t *testing.T) {
	if _, err := exec.LookPath("launchctl"); err != nil {
		t.Skip("no launchctl")
	}
	if out, err := Exec("version"); err != nil && len(out) == 0 {
		t.Errorf("launchctl version: %v", err)
	}
}
