//go:build darwin && cgo

package platformkeys

/*
#cgo LDFLAGS: -framework ApplicationServices

#include <ApplicationServices/ApplicationServices.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

#define PM_MAX_BINDINGS 512
#define PM_STOP_CODE UINT16_MAX

typedef struct {
	uint16_t key_code;
	uint16_t modifiers;
} pm_key_event;

typedef struct {
	uint16_t key_code;
	uint16_t modifiers;
} pm_binding;

static pm_binding pm_bindings[PM_MAX_BINDINGS];
static size_t pm_binding_count = 0;
static pthread_mutex_t pm_bindings_lock = PTHREAD_MUTEX_INITIALIZER;
static volatile int pm_enabled = 0;
static volatile int pm_should_stop = 0;
static int pm_prompted = 0;
static int pm_pipe[2] = {-1, -1};
static CFMachPortRef pm_event_tap = NULL;
static CFRunLoopRef pm_run_loop = NULL;

static uint16_t pm_modifiers(CGEventFlags flags) {
	uint16_t modifiers = 0;
	if ((flags & kCGEventFlagMaskAlternate) != 0) {
		modifiers |= 1;
	}
	if ((flags & kCGEventFlagMaskControl) != 0) {
		modifiers |= 2;
	}
	if ((flags & kCGEventFlagMaskShift) != 0) {
		modifiers |= 4;
	}
	if ((flags & kCGEventFlagMaskCommand) != 0) {
		modifiers |= 8;
	}
	return modifiers;
}

static bool pm_matches(uint16_t key_code, uint16_t modifiers) {
	bool matched = false;
	pthread_mutex_lock(&pm_bindings_lock);
	for (size_t i = 0; i < pm_binding_count; i++) {
		if (pm_bindings[i].key_code == key_code &&
		    pm_bindings[i].modifiers == modifiers) {
			matched = true;
			break;
		}
	}
	pthread_mutex_unlock(&pm_bindings_lock);
	return matched;
}

static CGEventRef pm_event_callback(CGEventTapProxy proxy, CGEventType type,
				    CGEventRef event, void *refcon) {
	(void)proxy;
	(void)refcon;
	if (type == kCGEventTapDisabledByTimeout ||
	    type == kCGEventTapDisabledByUserInput) {
		if (pm_event_tap != NULL) {
			CGEventTapEnable(pm_event_tap, true);
		}
		return event;
	}
	if (!pm_enabled || type != kCGEventKeyDown || pm_pipe[1] == -1) {
		return event;
	}

	pm_key_event key_event;
	key_event.key_code = (uint16_t)CGEventGetIntegerValueField(
		event, kCGKeyboardEventKeycode);
	key_event.modifiers = pm_modifiers(CGEventGetFlags(event));
	if (!pm_matches(key_event.key_code, key_event.modifiers)) {
		return event;
	}

	// The pipe is non-blocking: a busy consumer may drop a repeat event, but
	// the event-tap callback never stalls the system input pipeline.
	(void)write(pm_pipe[1], &key_event, sizeof(key_event));
	return NULL;
}

static bool pm_request_accessibility(void) {
	if (pm_prompted) {
		return AXIsProcessTrusted();
	}
	pm_prompted = 1;
	const void *keys[] = {kAXTrustedCheckOptionPrompt};
	const void *values[] = {kCFBooleanTrue};
	CFDictionaryRef options = CFDictionaryCreate(
		kCFAllocatorDefault, keys, values, 1,
		&kCFCopyStringDictionaryKeyCallBacks,
		&kCFTypeDictionaryValueCallBacks);
	bool trusted = AXIsProcessTrustedWithOptions(options);
	CFRelease(options);
	return trusted;
}

static void pm_keytap_replace(uint16_t *key_codes, uint16_t *modifiers,
			      size_t count) {
	if (count > PM_MAX_BINDINGS) {
		count = PM_MAX_BINDINGS;
	}
	pthread_mutex_lock(&pm_bindings_lock);
	pm_binding_count = count;
	for (size_t i = 0; i < count; i++) {
		pm_bindings[i].key_code = key_codes[i];
		pm_bindings[i].modifiers = modifiers[i];
	}
	pthread_mutex_unlock(&pm_bindings_lock);
}

// Returns 0 on success, 1 when permission is missing, and 2 when the event tap
// could not be created.
static int pm_keytap_prepare(void) {
	if (!pm_request_accessibility()) {
		return 1;
	}
	pm_should_stop = 0;
	if (pipe(pm_pipe) != 0) {
		pm_pipe[0] = -1;
		pm_pipe[1] = -1;
		return 2;
	}
	int flags = fcntl(pm_pipe[1], F_GETFL, 0);
	if (flags >= 0) {
		(void)fcntl(pm_pipe[1], F_SETFL, flags | O_NONBLOCK);
	}
	CGEventMask mask = CGEventMaskBit(kCGEventKeyDown);
	pm_event_tap = CGEventTapCreate(
		kCGSessionEventTap, kCGHeadInsertEventTap,
		kCGEventTapOptionDefault, mask, pm_event_callback, NULL);
	if (pm_event_tap == NULL) {
		close(pm_pipe[0]);
		close(pm_pipe[1]);
		pm_pipe[0] = -1;
		pm_pipe[1] = -1;
		return 2;
	}
	return 0;
}

static void pm_keytap_run(void) {
	CFRunLoopSourceRef source = CFMachPortCreateRunLoopSource(
		kCFAllocatorDefault, pm_event_tap, 0);
	pm_run_loop = CFRunLoopGetCurrent();
	CFRetain(pm_run_loop);
	CFRunLoopAddSource(pm_run_loop, source, kCFRunLoopCommonModes);
	CGEventTapEnable(pm_event_tap, true);
	if (!pm_should_stop) {
		CFRunLoopRun();
	}
	CFRunLoopRemoveSource(pm_run_loop, source, kCFRunLoopCommonModes);
	CFRelease(source);
	CFRelease(pm_run_loop);
	pm_run_loop = NULL;
}

static int pm_keytap_read(pm_key_event *event) {
	if (pm_pipe[0] == -1) {
		return 0;
	}
	return (int)read(pm_pipe[0], event, sizeof(*event));
}

static void pm_keytap_set_enabled(int enabled) {
	pm_enabled = enabled;
}

static void pm_keytap_stop(void) {
	pm_enabled = 0;
	pm_should_stop = 1;
	if (pm_pipe[1] != -1) {
		pm_key_event stop_event = {PM_STOP_CODE, 0};
		(void)write(pm_pipe[1], &stop_event, sizeof(stop_event));
	}
	if (pm_run_loop != NULL) {
		CFRunLoopStop(pm_run_loop);
	}
}

static void pm_keytap_cleanup(void) {
	if (pm_event_tap != NULL) {
		CFMachPortInvalidate(pm_event_tap);
		CFRelease(pm_event_tap);
		pm_event_tap = NULL;
	}
	if (pm_pipe[0] != -1) {
		close(pm_pipe[0]);
		pm_pipe[0] = -1;
	}
	if (pm_pipe[1] != -1) {
		close(pm_pipe[1]);
		pm_pipe[1] = -1;
	}
}
*/
import "C"

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// Available reports that this Darwin build includes the CGEvent adapter.
func Available() bool {
	return true
}

// NewSource creates the process-local macOS physical-key source.
func NewSource() Source {
	return newDarwinSource(readNativeKeyEvent)
}

func newDarwinSource(readEvent keyEventReader) *darwinSource {
	return &darwinSource{
		events:    make(chan string, 32),
		ready:     make(chan struct{}),
		byEvent:   map[keyEvent]string{},
		readEvent: readEvent,
	}
}

type keyEvent struct {
	keyCode   uint16
	modifiers Modifiers
}

type keyEventReader func() (keyEvent, bool)

type darwinSource struct {
	mu        sync.RWMutex
	events    chan string
	ready     chan struct{}
	readyOnce sync.Once
	byEvent   map[keyEvent]string
	readEvent keyEventReader
}

func (s *darwinSource) Replace(bindings []Binding) error {
	if len(bindings) > C.PM_MAX_BINDINGS {
		return fmt.Errorf("native key binding count %d exceeds limit %d", len(bindings), C.PM_MAX_BINDINGS)
	}
	codes := make([]C.uint16_t, len(bindings))
	modifiers := make([]C.uint16_t, len(bindings))
	byEvent := make(map[keyEvent]string, len(bindings))
	for i, binding := range bindings {
		codes[i] = C.uint16_t(binding.KeyCode)
		modifiers[i] = C.uint16_t(binding.Modifiers)
		byEvent[keyEvent{keyCode: binding.KeyCode, modifiers: binding.Modifiers}] = binding.Chord
	}

	s.mu.Lock()
	s.byEvent = byEvent
	s.mu.Unlock()

	if len(bindings) == 0 {
		C.pm_keytap_replace(nil, nil, 0)
		return nil
	}
	C.pm_keytap_replace(
		(*C.uint16_t)(unsafe.Pointer(&codes[0])),
		(*C.uint16_t)(unsafe.Pointer(&modifiers[0])),
		C.size_t(len(bindings)),
	)
	return nil
}

func (s *darwinSource) SetEnabled(enabled bool) {
	if enabled {
		C.pm_keytap_set_enabled(1)
		return
	}
	C.pm_keytap_set_enabled(0)
}

func (s *darwinSource) Ready() <-chan struct{} {
	return s.ready
}

func (s *darwinSource) Events() <-chan string {
	return s.events
}

func (s *darwinSource) Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	switch status := int(C.pm_keytap_prepare()); status {
	case 0:
	case 1:
		return ErrPermissionRequired
	default:
		return fmt.Errorf("create macOS CGEvent key tap")
	}
	s.readyOnce.Do(func() { close(s.ready) })
	defer C.pm_keytap_cleanup()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.readEvents()
	}()
	stopDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			C.pm_keytap_stop()
		case <-stopDone:
		}
	}()

	C.pm_keytap_run()
	close(stopDone)
	C.pm_keytap_stop()
	<-readDone
	return nil
}

func (s *darwinSource) readEvents() {
	for {
		event, ok := s.readEvent()
		if !ok {
			return
		}
		s.mu.RLock()
		chord := s.byEvent[event]
		s.mu.RUnlock()
		if chord == "" {
			continue
		}
		select {
		case s.events <- chord:
		default:
		}
	}
}

func readNativeKeyEvent() (keyEvent, bool) {
	var event C.pm_key_event
	if int(C.pm_keytap_read(&event)) != int(C.sizeof_pm_key_event) {
		return keyEvent{}, false
	}
	if uint16(event.key_code) == uint16(C.PM_STOP_CODE) {
		return keyEvent{}, false
	}
	return keyEvent{
		keyCode:   uint16(event.key_code),
		modifiers: Modifiers(event.modifiers),
	}, true
}
