//go:build darwin && cgo

package notify

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>
#include <stdlib.h>
#include <string.h>

static int has_bundle(void) {
	return [[NSBundle mainBundle] bundleIdentifier] != nil;
}

static char *copy_message(NSString *s) {
	return strdup(s.UTF8String ? s.UTF8String : "unknown error");
}

static BOOL wait_for(dispatch_semaphore_t sem, double seconds) {
	return dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(seconds * NSEC_PER_SEC))) == 0;
}

// authorize asks to show alerts and waits up to wait seconds for the answer:
// 1 allowed, 2 denied, 0 not answered in time. Errors set *err.
static int authorize(double wait, char **err) {
	@try {
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		__block int result = 0;
		__block NSString *msg = nil;
		UNAuthorizationOptions opts = UNAuthorizationOptionAlert | UNAuthorizationOptionSound;
		[[UNUserNotificationCenter currentNotificationCenter] requestAuthorizationWithOptions:opts
			completionHandler:^(BOOL granted, NSError *e) {
				result = granted ? 1 : 2;
				msg = e.localizedDescription;
				dispatch_semaphore_signal(sem);
			}];
		if (!wait_for(sem, wait)) {
			return 0;
		}
		if (msg != nil) {
			*err = copy_message(msg);
		}
		return result;
	} @catch (NSException *ex) {
		*err = copy_message(ex.reason);
		return 0;
	}
}

// send shows a notification if the app is allowed to, and waits up to 10 s
// for it to be accepted. It returns NULL or an error message.
static char *send(const char *title, const char *body) {
	@try {
		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		__block UNAuthorizationStatus status = UNAuthorizationStatusNotDetermined;
		[center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *s) {
			status = s.authorizationStatus;
			dispatch_semaphore_signal(sem);
		}];
		if (!wait_for(sem, 10)) {
			return strdup("timed out reading notification settings");
		}
		if (status != UNAuthorizationStatusAuthorized && status != UNAuthorizationStatusProvisional) {
			return strdup("notifications aren't allowed; turn them on in System Settings > Notifications > ReminderSync");
		}

		UNMutableNotificationContent *content = [UNMutableNotificationContent new];
		content.title = [NSString stringWithUTF8String:title];
		content.body = [NSString stringWithUTF8String:body];
		content.sound = [UNNotificationSound defaultSound];
		UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:[NSUUID UUID].UUIDString
			content:content trigger:nil];
		__block NSString *msg = nil;
		[center addNotificationRequest:req withCompletionHandler:^(NSError *e) {
			msg = e.localizedDescription;
			dispatch_semaphore_signal(sem);
		}];
		if (!wait_for(sem, 10)) {
			return strdup("timed out showing the notification");
		}
		return msg != nil ? copy_message(msg) : NULL;
	} @catch (NSException *ex) {
		return copy_message(ex.reason);
	}
}
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

// Authorize asks whether the app may show notifications, the first time with
// a prompt, and waits up to wait for the answer.
func Authorize(wait time.Duration) (Permission, error) {
	if C.has_bundle() == 0 {
		return Pending, ErrNoApp
	}
	var msg *C.char
	r := C.authorize(C.double(wait.Seconds()), &msg)
	if msg != nil {
		defer C.free(unsafe.Pointer(msg))
		return Pending, errors.New("notify: " + C.GoString(msg))
	}
	switch r {
	case 1:
		return Allowed, nil
	case 2:
		return Denied, nil
	}
	return Pending, nil
}

// Send shows a notification with title and body, if the app is allowed to.
func Send(title, body string) error {
	if C.has_bundle() == 0 {
		return ErrNoApp
	}
	ct, cb := C.CString(title), C.CString(body)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	if msg := C.send(ct, cb); msg != nil {
		defer C.free(unsafe.Pointer(msg))
		return errors.New("notify: " + C.GoString(msg))
	}
	return nil
}
