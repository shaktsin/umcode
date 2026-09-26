//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Foundation -framework ServiceManagement

#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>
#import <ServiceManagement/ServiceManagement.h>

// The engine's LaunchAgent plist lives in UMCode.app/Contents/Library/LaunchAgents.
static NSString *const kPlist = @"com.umcode.engine.plist";

// 0 unsupported (not in a bundle / pre-13), 1 not registered, 2 enabled, 3 requires approval, 4 not found
static int ufServiceStatus(void) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSString *plist = [[[[NSBundle mainBundle] bundlePath] stringByAppendingPathComponent:@"Contents/Library/LaunchAgents"] stringByAppendingPathComponent:kPlist];
			if (![[NSFileManager defaultManager] fileExistsAtPath:plist]) return 0;
			SMAppService *svc = [SMAppService agentServiceWithPlistName:kPlist];
			switch (svc.status) {
				case SMAppServiceStatusEnabled: return 2;
				case SMAppServiceStatusRequiresApproval: return 3;
				case SMAppServiceStatusNotFound: return 4;
				default: return 1;
			}
		}
	}
	return 0;
}

static int ufServiceRegister(char **msg) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSError *err = nil;
			if ([[SMAppService agentServiceWithPlistName:kPlist] registerAndReturnError:&err]) return 0;
			if (err) *msg = strdup([[err localizedDescription] UTF8String]);
			return 1;
		}
	}
	return 2;
}

static int ufServiceUnregister(char **msg) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSError *err = nil;
			if ([[SMAppService agentServiceWithPlistName:kPlist] unregisterAndReturnError:&err]) return 0;
			if (err) *msg = strdup([[err localizedDescription] UTF8String]);
			return 1;
		}
	}
	return 2;
}

static void ufOpenLoginItems(void) {
	if (@available(macOS 13.0, *)) {
		[SMAppService openSystemSettingsLoginItems];
	}
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"unsafe"
)

func serviceStatus() serviceState {
	switch C.ufServiceStatus() {
	case 1, 4:
		return serviceNotRegistered
	case 2:
		return serviceEnabled
	case 3:
		return serviceRequiresApproval
	}
	return serviceUnsupported
}

func registerService() error {
	if serviceStatus() == serviceUnsupported {
		return errServiceUnsupported
	}
	var msg *C.char
	rc := C.ufServiceRegister(&msg)
	return smErr(rc, msg, "register")
}

func unregisterService() error {
	if serviceStatus() == serviceUnsupported {
		return errServiceUnsupported
	}
	var msg *C.char
	rc := C.ufServiceUnregister(&msg)
	return smErr(rc, msg, "unregister")
}

func smErr(rc C.int, msg *C.char, op string) error {
	if msg != nil {
		defer C.free(unsafe.Pointer(msg))
	}
	switch rc {
	case 0:
		return nil
	case 2:
		return errServiceUnsupported
	}
	if msg != nil {
		return fmt.Errorf("background service %s: %s", op, C.GoString(msg))
	}
	return errors.New("background service " + op + " failed")
}

// restartService asks launchd to restart the agent (KeepAlive brings it back).
func restartService() error {
	out, err := exec.Command("/bin/launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, out)
	}
	return nil
}

func openLoginItemsSettings() { C.ufOpenLoginItems() }
