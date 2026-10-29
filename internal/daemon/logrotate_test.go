//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openLog opens path for appending, like launchd does for the daemon's
// stdout and stderr.
func openLog(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotateLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	stdout, stderr := openLog(t, path), openLog(t, path)
	if _, err := stdout.WriteString("first line\n"); err != nil {
		t.Fatal(err)
	}

	// Under the limit, nothing changes.
	rotated, err := RotateLog(path, 100, stdout, stderr)
	if err != nil || rotated {
		t.Fatalf("rotated = %t, err = %v under the limit", rotated, err)
	}

	if _, err := stdout.WriteString(strings.Repeat("x", 100) + "\n"); err != nil {
		t.Fatal(err)
	}
	rotated, err = RotateLog(path, 100, stdout, stderr)
	if err != nil || !rotated {
		t.Fatalf("rotated = %t, err = %v over the limit", rotated, err)
	}
	if got := read(t, path+".1"); !strings.HasPrefix(got, "first line\n") {
		t.Errorf("daemon.log.1 = %q, want the old log", got)
	}

	// Both outputs now write to the new file.
	if _, err := stdout.WriteString("after\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.WriteString("panic: oops\n"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "after\npanic: oops\n" {
		t.Errorf("daemon.log = %q, want only what was written after rotating", got)
	}

	// Rotating again replaces the older copy.
	if _, err := stdout.WriteString(strings.Repeat("y", 100) + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateLog(path, 100, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path+".1"); !strings.HasPrefix(got, "after\n") {
		t.Errorf("daemon.log.1 = %q, want the second log", got)
	}
}

func TestRotateLogLeavesOtherOutputsAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Like a daemon run in a terminal: stdout isn't the log.
	other := openLog(t, filepath.Join(dir, "terminal"))
	if rotated, err := RotateLog(path, 100, other); err != nil || rotated {
		t.Errorf("rotated = %t, err = %v for an output that isn't the log", rotated, err)
	}
	if rotated, err := RotateLog(filepath.Join(dir, "missing.log"), 100, other); err != nil || rotated {
		t.Errorf("rotated = %t, err = %v for a log that doesn't exist", rotated, err)
	}
	if rotated, err := RotateLog(path, 100); err != nil || rotated {
		t.Errorf("rotated = %t, err = %v with no outputs", rotated, err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("rotated anyway: %v", err)
	}
}

// bigLog makes a log over the 100-byte limit the error tests use, open as
// the daemon's output.
func bigLog(t *testing.T) (path string, out *os.File) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "daemon.log")
	out = openLog(t, path)
	if _, err := out.WriteString(strings.Repeat("x", 200)); err != nil {
		t.Fatal(err)
	}
	return path, out
}

func TestRotateLogErrors(t *testing.T) {
	t.Run("closed output", func(t *testing.T) {
		path, _ := bigLog(t)
		closed := openLog(t, path+".other")
		_ = closed.Close()
		if _, err := RotateLog(path, 100, closed); err == nil {
			t.Error("a closed output didn't fail")
		}
	})

	t.Run("output that can't be redirected", func(t *testing.T) {
		path, out := bigLog(t)
		closed := openLog(t, path+".other")
		_ = closed.Close()
		// The first output decides whether to rotate, so this one goes second.
		if _, err := RotateLog(path, 100, out, closed); err == nil {
			t.Error("a failed dup2 didn't fail")
		}
	})

	t.Run("new log can't be opened", func(t *testing.T) {
		path, out := bigLog(t)
		orig := openFile
		t.Cleanup(func() { openFile = orig })
		openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, os.ErrPermission }
		if _, err := RotateLog(path, 100, out); err == nil {
			t.Error("a failed open didn't fail")
		}
	})

	t.Run("rename blocked", func(t *testing.T) {
		path, out := bigLog(t)
		if err := os.Mkdir(path+".1", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path+".1", "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := RotateLog(path, 100, out); err == nil {
			t.Error("a failed rename didn't fail")
		}
	})
}
