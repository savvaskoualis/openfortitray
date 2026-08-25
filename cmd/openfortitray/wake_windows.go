//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// deviceNotifyCallback selects PowerRegisterSuspendResumeNotification's
// callback-based registration (rather than a window handle), so this needs no
// message-pump window of its own — the app is a background tray process with no
// window to hook WM_POWERBROADCAST into.
const deviceNotifyCallback = 2

// pbtAPMResumeAutomatic is the PBT_APMRESUMEAUTOMATIC event: delivered every
// time the system resumes from sleep or hibernation, regardless of whether a
// user action triggered the resume. PBT_APMRESUMESUSPEND exists too but only
// fires after a user-triggered resume, which is not a distinction that matters
// here — a stale tunnel is stale either way.
const pbtAPMResumeAutomatic = 0x12

var (
	powrprof                       = windows.NewLazySystemDLL("powrprof.dll")
	procPowerRegisterSuspendResume = powrprof.NewProc("PowerRegisterSuspendResumeNotification")
)

// deviceNotifySubscribeParameters mirrors Win32's
// DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS: a callback function pointer plus an
// opaque context value passed back on every invocation.
type deviceNotifySubscribeParameters struct {
	callback uintptr
	context  uintptr
}

// onSystemWakeFn is what a system wake runs. Set by watchSystemSleep before
// registration; nil means "do nothing", so a callback before wiring cannot
// panic.
var onSystemWakeFn func()

// subscribeParams is held in a package variable, not a local, because the OS
// keeps a pointer to it for the lifetime of the registration and Go's GC has no
// way to know that.
var subscribeParams deviceNotifySubscribeParameters

// deviceNotifyCallbackRoutine matches Win32's DEVICE_NOTIFY_CALLBACK_ROUTINE
// signature: (PVOID Context, ULONG Type, PVOID Setting) -> ULONG. It runs on a
// thread the OS provides, never the pump goroutine — see onSystemWake's doc
// comment on why the app tracks wantConnected instead of reading pump state
// here.
func deviceNotifyCallbackRoutine(context, eventType, setting uintptr) uintptr {
	if eventType == pbtAPMResumeAutomatic && onSystemWakeFn != nil {
		onSystemWakeFn()
	}
	return 0
}

// watchSystemSleep makes fn run whenever the system resumes from sleep.
// Best-effort: a registration failure (e.g. an unsupported Windows version)
// only costs the automatic reconnect-on-wake, not the app's ability to run.
func watchSystemSleep(fn func()) {
	onSystemWakeFn = fn
	subscribeParams = deviceNotifySubscribeParameters{
		callback: syscall.NewCallback(deviceNotifyCallbackRoutine),
	}
	var handle uintptr
	procPowerRegisterSuspendResume.Call(
		uintptr(deviceNotifyCallback),
		uintptr(unsafe.Pointer(&subscribeParams)),
		uintptr(unsafe.Pointer(&handle)),
	)
}
