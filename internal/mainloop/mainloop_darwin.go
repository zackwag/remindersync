//go:build darwin

// Package mainloop runs a command off the main thread so the main thread can
// run the Core Foundation run loop. EventKit delivers
// EKEventStoreChangedNotification for changes made by other processes, such
// as Reminders.app and iCloud sync, only to a process whose main run loop is
// running. Without it, go-eventkit's WatchChanges sees only our own writes.
package mainloop

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>

static void never(CFRunLoopTimerRef timer, void *info) {}

static void run_main_loop(void) {
	// A run loop with no sources returns at once, so give it a timer that
	// never fires.
	CFRunLoopTimerRef t = CFRunLoopTimerCreate(NULL, CFAbsoluteTimeGetCurrent() + 1e10, 1e10, 0, 0, never, NULL);
	CFRunLoopAddTimer(CFRunLoopGetMain(), t, kCFRunLoopCommonModes);
	CFRunLoopRun();
}
*/
import "C"

import (
	"os"
	"runtime"
)

// Locking in init keeps the main goroutine on the main thread.
func init() {
	runtime.LockOSThread()
}

// Run runs fn on another goroutine while the main thread runs the run loop,
// then exits the process with fn's result. Call it from main.
func Run(fn func() int) {
	go func() { os.Exit(fn()) }()
	C.run_main_loop()
}
