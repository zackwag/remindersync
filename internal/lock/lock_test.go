//go:build unix

package lock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sync.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}

	// flock locks are per open file, so a second open in this process
	// conflicts just like another process would.
	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second Acquire: err = %v, want ErrHeld", err)
	}

	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l, err = Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	_ = l.Release()
}

// TestHelperHoldsLock is run as a separate process by
// TestHeldByAnotherProcess. It takes the lock, says so, and holds it until its
// stdin closes.
func TestHelperHoldsLock(t *testing.T) {
	path := os.Getenv("LOCK_HELPER_PATH")
	if path == "" {
		t.Skip("only runs as a helper process")
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestHeldByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsLock$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper said %q, %v", line, err)
	}

	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Errorf("while the helper holds it: err = %v, want ErrHeld", err)
	}

	// Ending the helper releases its lock.
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("after the helper exited: %v", err)
	}
	_ = l.Release()
}

func TestAcquireErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(filepath.Join(file, "sync.lock")); err == nil {
		t.Error("want an error when the directory can't be created")
	}
	if _, err := Acquire(t.TempDir()); err == nil {
		t.Error("want an error when the path is a directory")
	}
}
