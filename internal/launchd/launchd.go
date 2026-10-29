// Package launchd writes, loads and unloads the launchd user agent that runs
// `remindersync daemon`.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Label names the agent.
const Label = "com.zackwag.remindersync"

// BundleID is ReminderSync.app's bundle identifier, from packaging/Info.plist.
const BundleID = "dev.zackwag.ReminderSync"

// Agent describes the launchd job.
type Agent struct {
	Label string
	// Program is the command and its arguments.
	Program []string
	// LogPath gets both stdout and stderr.
	LogPath string
	// BundleID, if set, is the app the program belongs to, so System Settings
	// shows the agent under the app's name in Login Items.
	BundleID string
}

// Plist renders the agent's property list. The job:
//   - starts at login (RunAtLoad) and only in a GUI session (Aqua), since
//     the Reminders permission prompt needs one;
//   - is restarted after a crash or error exit, but not after a clean exit,
//     which is how the daemon stops for good when Reminders access is
//     refused;
//   - waits 30 s between restarts, so a crash loop doesn't spin.
func (a Agent) Plist() []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key(&b, "Label")
	str(&b, "\t", a.Label)
	if a.BundleID != "" {
		key(&b, "AssociatedBundleIdentifiers")
		b.WriteString("\t<array>\n")
		str(&b, "\t\t", a.BundleID)
		b.WriteString("\t</array>\n")
	}
	key(&b, "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range a.Program {
		str(&b, "\t\t", arg)
	}
	b.WriteString("\t</array>\n")
	key(&b, "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key(&b, "KeepAlive")
	b.WriteString("\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	key(&b, "ThrottleInterval")
	b.WriteString("\t<integer>30</integer>\n")
	key(&b, "LimitLoadToSessionType")
	str(&b, "\t", "Aqua")
	key(&b, "StandardOutPath")
	str(&b, "\t", a.LogPath)
	key(&b, "StandardErrorPath")
	str(&b, "\t", a.LogPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func key(b *bytes.Buffer, k string) {
	fmt.Fprintf(b, "\t<key>%s</key>\n", k)
}

func str(b *bytes.Buffer, indent, s string) {
	b.WriteString(indent + "<string>")
	_ = xml.EscapeText(b, []byte(s))
	b.WriteString("</string>\n")
}

// InBundle reports whether exe is an app bundle's main executable, in
// Contents/MacOS, and returns the bundle's path.
func InBundle(exe string) (string, bool) {
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	ok := filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(app, ".app")
	return app, ok
}

// Launchctl runs launchctl with args and returns its combined output.
type Launchctl func(args ...string) ([]byte, error)

// Exec is the real launchctl.
func Exec(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

// Domain is the logged-in user's GUI domain, such as gui/501.
func Domain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// bootstrapTries and bootstrapWait cover launchd still tearing down the old
// job right after bootout, which fails the bootstrap with an I/O error.
var (
	bootstrapTries = 5
	bootstrapWait  = time.Second
)

// Load loads the agent from plistPath, unloading any copy that's already
// loaded first so a reinstall picks up the new binary and plist.
func Load(run Launchctl, plistPath, label string) error {
	// Fails when the job isn't loaded, which is fine.
	_, _ = run("bootout", Domain()+"/"+label)
	var err error
	for i := range bootstrapTries {
		if i > 0 {
			time.Sleep(bootstrapWait)
		}
		var out []byte
		if out, err = run("bootstrap", Domain(), plistPath); err == nil {
			return nil
		}
		err = fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}

// Unload stops the agent and unloads it. It reports whether it was loaded.
func Unload(run Launchctl, label string) (bool, error) {
	if _, err := run("print", Domain()+"/"+label); err != nil {
		return false, nil
	}
	if out, err := run("bootout", Domain()+"/"+label); err != nil {
		return true, fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
