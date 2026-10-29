//go:build unix

package daemon

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openFile opens the new log. Tests replace it, since nothing else can make
// it fail right after renaming the old log out of the same directory.
var openFile = os.OpenFile

// RotateLog keeps the daemon's log from growing without bound. launchd opens
// the log once, as the daemon's stdout and stderr, so the daemon can't simply
// reopen it. Instead, when outs[0] is the file at path and it's grown past
// limit bytes, RotateLog renames it to path.1, replacing any older one, opens a
// new file at path, and points every file in outs at it with dup2. That
// keeps at most about twice limit on disk. It reports whether it rotated.
//
// When outs[0] isn't the file at path, such as when the daemon runs in a
// terminal, it does nothing.
func RotateLog(path string, limit int64, outs ...*os.File) (bool, error) {
	if len(outs) == 0 {
		return false, nil
	}
	cur, err := outs[0].Stat()
	if err != nil {
		return false, err
	}
	onDisk, err := os.Stat(path)
	if err != nil || !os.SameFile(cur, onDisk) || cur.Size() < limit {
		return false, nil
	}

	if err := os.Rename(path, path+".1"); err != nil {
		return false, fmt.Errorf("rotate log: %w", err)
	}
	f, err := openFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Errorf("rotate log: %w", err)
	}
	defer func() { _ = f.Close() }()
	for _, out := range outs {
		if err := unix.Dup2(int(f.Fd()), int(out.Fd())); err != nil {
			return false, fmt.Errorf("rotate log: %w", err)
		}
	}
	return true, nil
}
