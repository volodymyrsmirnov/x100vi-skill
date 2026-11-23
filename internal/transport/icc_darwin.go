//go:build darwin && cgo

package transport

// macOS: PTP through ImageCaptureCore, macOS' native camera interface.
//
// ptpcamerad owns every PTP camera and keeps the PTP session open. ICCameraDevice
// requestSendPTPCommand:outData:completion: takes a complete PTP command container plus the raw bytes of
// an outgoing data phase, and hands back the incoming data and the response container. Nothing has to be
// stopped, and other apps can stay connected.
//
// ImageCaptureCore delivers its callbacks through the main run loop, which the shim below spins while it
// waits. So the transport must be used from the main goroutine: init locks it to the main OS thread, and
// a call from any other thread fails with an error instead of waiting for callbacks that never come.
// Every callback the shim waits for signals a run-loop source as soon as it has stored its result, so
// the spin returns at once instead of at the end of its time slice.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework ImageCaptureCore
#import <Foundation/Foundation.h>
#import <ImageCaptureCore/ImageCaptureCore.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

enum {
	XI_OK = 0,
	XI_NOT_MAIN_THREAD = 1,
	XI_NOT_FOUND = 2,
	XI_OPEN_TIMEOUT = 3,
	XI_OPEN_ERROR = 4,
	XI_REMOVED = 5,
	XI_TIMEOUT = 6,
	XI_ERROR = 7,
	XI_CLOSE_TIMEOUT = 8,
	XI_CLOSE_PENDING = 9,
};

static void x_wake(void);

typedef struct {
	void *data;
	size_t dataLen;
	void *response;
	size_t responseLen;
	char *message;
} x_reply;

// X100VIGoICCClose waits for one device's session to close.
@interface X100VIGoICCClose : NSObject
@property (atomic, strong) ICDevice *device;
@property (atomic) BOOL done;
@property (atomic, strong) NSError *error;
@end

@implementation X100VIGoICCClose
@end

// The delegate also keeps the close bookkeeping. A close waits for didCloseSessionWithError:; when that
// times out, the device is marked close-pending, and the callback that arrives later only clears that
// mark: it must not be taken for the answer to a later close. The next open of a close-pending device
// waits for that callback (or the device's removal) first. Each callback is consumed once: by the
// pending mark of its device, else by the close currently waiting for that device, else by nothing.
// Removal is tracked per device too: only the removal of the device a close is for ends that close, or
// clears its pending mark, and only a removed device fails its requests as disconnected.
@interface X100VIGoICCDelegate : NSObject <ICDeviceBrowserDelegate, ICCameraDeviceDelegate>
@property (atomic, strong) NSMutableArray *devices;
@property (atomic) BOOL opened;
@property (atomic, strong) NSError *openError;
@property (atomic, strong) X100VIGoICCClose *closing;
@property (atomic, strong) NSHashTable *closePending;
@property (atomic, strong) NSHashTable *removedDevices;
@end

@implementation X100VIGoICCDelegate
- (instancetype)init {
	self = [super init];
	if (self) {
		_devices = [NSMutableArray array];
		_closePending = [NSHashTable hashTableWithOptions:
			NSPointerFunctionsStrongMemory | NSPointerFunctionsObjectPointerPersonality];
		// Weak: a removed device object is not kept alive by its mark, and the mark goes with it.
		_removedDevices = [NSHashTable hashTableWithOptions:
			NSPointerFunctionsWeakMemory | NSPointerFunctionsObjectPointerPersonality];
	}
	return self;
}
- (BOOL)hasDevice:(ICDevice *)device {
	@synchronized (self) { return [self.devices indexOfObjectIdenticalTo:device] != NSNotFound; }
}
- (BOOL)isClosePending:(ICDevice *)device {
	@synchronized (self) { return [self.closePending containsObject:device]; }
}
- (BOOL)isRemoved:(ICDevice *)device {
	@synchronized (self) { return [self.removedDevices containsObject:device]; }
}
// markRemoved records that device is gone: its pending close is over, and so is a close waiting for it.
- (void)markRemoved:(ICDevice *)device {
	@synchronized (self) {
		[self.removedDevices addObject:device];
		[self.closePending removeObject:device];
	}
}
// clearRemoved forgets a removal mark before device is opened again.
- (void)clearRemoved:(ICDevice *)device {
	@synchronized (self) { [self.removedDevices removeObject:device]; }
}
// beginClose makes a waiter for device's close callback the current one.
- (X100VIGoICCClose *)beginClose:(ICDevice *)device {
	X100VIGoICCClose *closing = [[X100VIGoICCClose alloc] init];
	closing.device = device;
	@synchronized (self) { self.closing = closing; }
	return closing;
}
// endClose stops waiting. A close that got no callback, of a device that was not removed, leaves the
// device close-pending. It returns whether the close is over (confirmed, or that device is gone).
- (BOOL)endClose:(X100VIGoICCClose *)closing {
	@synchronized (self) {
		if (self.closing == closing) {
			self.closing = nil;
		}
		if (closing.done || [self.removedDevices containsObject:closing.device]) {
			return YES;
		}
		[self.closePending addObject:closing.device];
		return NO;
	}
}
// forgetDevice is the browser's removal: the device is no longer listed, and is marked removed.
- (void)forgetDevice:(ICDevice *)device {
	@synchronized (self) {
		[self.devices removeObjectIdenticalTo:device];
		[self.removedDevices addObject:device];
		[self.closePending removeObject:device];
	}
}
// ICDeviceBrowserDelegate
- (void)deviceBrowser:(ICDeviceBrowser *)browser didAddDevice:(ICDevice *)device moreComing:(BOOL)more {
	@synchronized (self) { [self.devices addObject:device]; }
	x_wake();
}
- (void)deviceBrowser:(ICDeviceBrowser *)browser didRemoveDevice:(ICDevice *)device moreGoing:(BOOL)more {
	[self forgetDevice:device];
	x_wake();
}
// ICDeviceDelegate
- (void)device:(ICDevice *)device didOpenSessionWithError:(NSError *)error {
	self.openError = error;
	self.opened = YES;
	x_wake();
}
- (void)device:(ICDevice *)device didCloseSessionWithError:(NSError *)error {
	@synchronized (self) {
		if ([self.closePending containsObject:device]) {
			[self.closePending removeObject:device]; // the late answer to a close that timed out
		} else if (self.closing != nil && self.closing.device == device && !self.closing.done) {
			X100VIGoICCClose *closing = self.closing;
			self.closing = nil;
			closing.error = error;
			closing.done = YES;
		}
	}
	x_wake();
}
- (void)didRemoveDevice:(ICDevice *)device {
	[self markRemoved:device];
	x_wake();
}
// ICCameraDeviceDelegate (required methods; the content catalog is not used)
- (void)cameraDevice:(ICCameraDevice *)camera didAddItems:(NSArray<ICCameraItem *> *)items {}
- (void)cameraDevice:(ICCameraDevice *)camera didRemoveItems:(NSArray<ICCameraItem *> *)items {}
- (void)cameraDevice:(ICCameraDevice *)camera didRenameItems:(NSArray<ICCameraItem *> *)items {}
- (void)cameraDevice:(ICCameraDevice *)camera didReceiveThumbnail:(CGImageRef)thumbnail forItem:(ICCameraItem *)item error:(NSError *)error {}
- (void)cameraDevice:(ICCameraDevice *)camera didReceiveMetadata:(NSDictionary *)metadata forItem:(ICCameraItem *)item error:(NSError *)error {}
- (void)cameraDevice:(ICCameraDevice *)camera didReceivePTPEvent:(NSData *)eventData {}
- (void)cameraDeviceDidChangeCapability:(ICCameraDevice *)camera {}
- (void)cameraDeviceDidRemoveAccessRestriction:(ICDevice *)device {}
- (void)cameraDeviceDidEnableAccessRestriction:(ICDevice *)device {}
- (void)deviceDidBecomeReadyWithCompleteContentCatalog:(ICCameraDevice *)device {}
@end

@interface X100VIGoICCReply : NSObject
@property (atomic) BOOL done;
@property (atomic, strong) NSData *data;
@property (atomic, strong) NSData *response;
@property (atomic, strong) NSError *error;
@end

@implementation X100VIGoICCReply
@end

// One browser per process, kept alive: a second browser is never told about devices.
static ICDeviceBrowser *gBrowser;
static X100VIGoICCDelegate *gDelegate;

// gWake is a run-loop source on the main run loop that callbacks signal once they have stored a result,
// created with the browser and kept for the life of the process. A signal is remembered until the run
// loop next runs, so one that arrives (on any thread) between x_spin's check and its next run is not
// lost, and one that arrives late, after a wait timed out, only makes a later wait look once more.
static CFRunLoopSourceRef gWake;

static void x_wake_perform(void *info) {}

static void x_wake(void) {
	CFRunLoopSourceRef source = gWake;
	if (source != NULL) {
		CFRunLoopSourceSignal(source);
		CFRunLoopWakeUp(CFRunLoopGetMain());
	}
}

static uint64_t x_now(void) { return clock_gettime_nsec_np(CLOCK_UPTIME_RAW); }

// x_spin runs the current thread's run loop until done() or the timeout. Each run returns as soon as a
// source was handled (x_wake signals one), so done() is checked again right after a callback.
static BOOL x_spin(BOOL (^done)(void), double timeout) {
	uint64_t end = x_now() + (uint64_t)(timeout * 1e9);
	while (!done()) {
		if (x_now() >= end) {
			return NO;
		}
		@autoreleasepool {
			if (CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.02, true) == kCFRunLoopRunFinished) {
				usleep(20000);
			}
		}
	}
	return YES;
}

static char *x_strdup(NSString *s) {
	const char *u = s.UTF8String;
	return strdup(u ? u : "");
}

static ICCameraDevice *x_find(void) {
	NSArray *devices;
	@synchronized (gDelegate) { devices = [gDelegate.devices copy]; }
	for (ICDevice *device in devices) {
		if (![device respondsToSelector:@selector(requestSendPTPCommand:outData:completion:)] ||
		    ![device respondsToSelector:@selector(usbVendorID)]) {
			continue;
		}
		if (device.usbVendorID == 0x04CB) {
			return (ICCameraDevice *)device;
		}
	}
	return nil;
}

static int x_icc_open(double findTimeout, double pendingTimeout, double openTimeout, uintptr_t *handle,
		char **message) {
	@autoreleasepool {
		*handle = 0;
		*message = NULL;
		if (!pthread_main_np()) {
			return XI_NOT_MAIN_THREAD;
		}
		if (gBrowser == nil) {
			CFRunLoopSourceContext context = {0};
			context.perform = x_wake_perform;
			gWake = CFRunLoopSourceCreate(kCFAllocatorDefault, 0, &context);
			CFRunLoopAddSource(CFRunLoopGetMain(), gWake, kCFRunLoopDefaultMode);
			gDelegate = [[X100VIGoICCDelegate alloc] init];
			gBrowser = [[ICDeviceBrowser alloc] init];
			gBrowser.delegate = gDelegate;
			// Cameras, at every location (ICDeviceLocationTypeMaskAll).
			gBrowser.browsedDeviceTypeMask = (ICDeviceTypeMask)(ICDeviceTypeMaskCamera | 0x0000FF00);
			[gBrowser start];
		}
		gDelegate.opened = NO;
		gDelegate.openError = nil;
		x_spin(^BOOL { return x_find() != nil; }, findTimeout);
		ICCameraDevice *camera = x_find();
		if (camera == nil) {
			return XI_NOT_FOUND;
		}
		if ([gDelegate isClosePending:camera]) {
			// An earlier close of this device timed out: wait for its late confirmation, or for the device
			// to go away, before opening it again.
			ICCameraDevice *pending = camera;
			x_spin(^BOOL { return ![gDelegate isClosePending:pending] || ![gDelegate hasDevice:pending]; },
				pendingTimeout);
			if ([gDelegate hasDevice:pending]) {
				if ([gDelegate isClosePending:pending]) {
					return XI_CLOSE_PENDING;
				}
			} else {
				x_spin(^BOOL { return x_find() != nil; }, findTimeout);
				camera = x_find();
				if (camera == nil) {
					return XI_NOT_FOUND;
				}
			}
		}
		[gDelegate clearRemoved:camera];
		camera.delegate = gDelegate;
		[camera requestOpenSession];
		if (!x_spin(^BOOL { return gDelegate.opened; }, openTimeout)) {
			return XI_OPEN_TIMEOUT;
		}
		NSError *error = gDelegate.openError;
		if (error != nil) {
			*message = x_strdup(error.localizedDescription);
			return XI_OPEN_ERROR;
		}
		*handle = (uintptr_t)CFBridgingRetain(camera);
		return XI_OK;
	}
}

static void *x_copy(NSData *data, size_t *length) {
	*length = 0;
	if (data == nil || data.length == 0) {
		return NULL;
	}
	void *out = malloc(data.length);
	if (out == NULL) {
		return NULL;
	}
	memcpy(out, data.bytes, data.length);
	*length = data.length;
	return out;
}

static int x_icc_send(uintptr_t handle, const void *command, size_t commandLen, const void *outData,
		size_t outLen, double timeout, x_reply *reply) {
	@autoreleasepool {
		memset(reply, 0, sizeof *reply);
		if (!pthread_main_np()) {
			return XI_NOT_MAIN_THREAD;
		}
		ICCameraDevice *camera = (__bridge ICCameraDevice *)(void *)handle;
		if ([gDelegate isRemoved:camera]) {
			return XI_REMOVED;
		}
		X100VIGoICCReply *box = [[X100VIGoICCReply alloc] init];
		NSData *cmd = [NSData dataWithBytes:command length:commandLen];
		NSData *out = outLen ? [NSData dataWithBytes:outData length:outLen] : [NSData data];
		[camera requestSendPTPCommand:cmd outData:out completion:^(NSData *data, NSData *response, NSError *error) {
			box.data = data;
			box.response = response;
			box.error = error;
			box.done = YES;
			x_wake();
		}];
		if (!x_spin(^BOOL { return box.done; }, timeout)) {
			return XI_TIMEOUT;
		}
		if (box.error != nil) {
			reply->message = x_strdup(box.error.localizedDescription);
			return XI_ERROR;
		}
		reply->data = x_copy(box.data, &reply->dataLen);
		reply->response = x_copy(box.response, &reply->responseLen);
		return XI_OK;
	}
}

// x_icc_close ends this app's session but keeps the browser. It waits for the device to confirm, until
// the timeout or the device's removal, and reports an error the device gave in *message.
static int x_icc_close(uintptr_t handle, double timeout, char **message) {
	@autoreleasepool {
		*message = NULL;
		ICCameraDevice *camera = CFBridgingRelease((void *)handle);
		if (!pthread_main_np()) {
			[camera requestCloseSession]; // still asked for, but its confirmation cannot arrive here
			return XI_NOT_MAIN_THREAD;
		}
		X100VIGoICCClose *closing = [gDelegate beginClose:camera];
		[camera requestCloseSession];
		x_spin(^BOOL { return closing.done || [gDelegate isRemoved:camera]; }, timeout);
		BOOL done = [gDelegate endClose:closing]; // on a timeout, marks the device close-pending
		if (closing.error != nil) {
			*message = x_strdup(closing.error.localizedDescription);
			return XI_ERROR;
		}
		if (!done) {
			return XI_CLOSE_TIMEOUT;
		}
		return XI_OK;
	}
}

// x_close_check runs the close bookkeeping on a fresh delegate, with stand-in objects for devices (only
// their identity is used), and returns 0 or the number of the first check that failed. For the tests,
// which cannot reach ImageCaptureCore's callbacks off the main thread.
static int x_close_check(void) {
	@autoreleasepool {
		X100VIGoICCDelegate *d = [[X100VIGoICCDelegate alloc] init];
		ICDevice *a = (ICDevice *)(id)[[NSObject alloc] init];
		ICDevice *b = (ICDevice *)(id)[[NSObject alloc] init];
		NSError *failure = [NSError errorWithDomain:@"x100vi" code:1 userInfo:nil];
		@synchronized (d) { [d.devices addObject:a]; [d.devices addObject:b]; }

		// A confirmed close: done, with the device's error, and nothing pending.
		X100VIGoICCClose *c = [d beginClose:a];
		[d device:a didCloseSessionWithError:failure];
		if (!c.done || c.error != failure || ![d endClose:c] || [d isClosePending:a]) return 1;

		// A callback with no close waiting is dropped.
		[d device:a didCloseSessionWithError:nil];
		if ([d isClosePending:a] || d.closing != nil) return 2;

		// A close that times out leaves the device close-pending.
		c = [d beginClose:a];
		if ([d endClose:c] || ![d isClosePending:a] || d.closing != nil) return 3;

		// Another device's callback does not answer a waiting close.
		X100VIGoICCClose *cb = [d beginClose:b];
		[d device:a didCloseSessionWithError:nil]; // a's late callback: clears a's mark only
		if (cb.done || [d isClosePending:a]) return 4;
		[d device:b didCloseSessionWithError:nil];
		if (!cb.done || ![d endClose:cb] || [d isClosePending:b]) return 5;

		// With a close of a pending and a close of a waiting at once, the first callback is the late one
		// and only clears the mark; the second answers the waiting close; a third is dropped.
		c = [d beginClose:a];
		[d endClose:c];
		X100VIGoICCClose *again = [d beginClose:a];
		[d device:a didCloseSessionWithError:failure];
		if (again.done || [d isClosePending:a]) return 6;
		[d device:a didCloseSessionWithError:nil];
		if (!again.done || again.error != nil || d.closing != nil) return 7;
		[d device:a didCloseSessionWithError:failure];
		if (again.error != nil || [d isClosePending:a]) return 8;
		if (![d endClose:again] || [d isClosePending:a]) return 9;

		// Removal is per device: another device's removal neither ends a waiting close nor clears a
		// pending mark, and only the removed device counts as disconnected.
		ICDevice *e = (ICDevice *)(id)[[NSObject alloc] init];
		@synchronized (d) { [d.devices addObject:e]; }
		c = [d beginClose:a];
		[d didRemoveDevice:e];
		if (c.done || [d isRemoved:a] || ![d isRemoved:e]) return 10;
		if ([d endClose:c] || ![d isClosePending:a]) return 11; // e's removal did not end a's close
		[d didRemoveDevice:e];
		if (![d isClosePending:a]) return 12; // nor clear a's pending mark
		c = [d beginClose:e];
		if (![d endClose:c] || [d isClosePending:e]) return 13; // a close of the removed device is over

		// The device's own removal clears its mark, and a close that ends with it removed is over.
		[d didRemoveDevice:a];
		if ([d isClosePending:a] || ![d isRemoved:a]) return 14;
		c = [d beginClose:a];
		if (![d endClose:c] || [d isClosePending:a]) return 15;
		[d clearRemoved:a]; // what an open of the device does
		if ([d isRemoved:a]) return 16;

		// The browser's removal does the same for its device, and only for it.
		c = [d beginClose:b];
		[d endClose:c];
		if (![d isClosePending:b]) return 17;
		c = [d beginClose:a];
		[d forgetDevice:b]; // what the browser's didRemoveDevice: does
		if ([d isClosePending:b] || [d hasDevice:b] || ![d isRemoved:b] || [d isRemoved:a]) return 18;
		if ([d endClose:c] || ![d isClosePending:a]) return 19;
		return 0;
	}
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/ptp"
	"github.com/volodymyrsmirnov/x100vi-skill/internal/sandbox"
)

// ImageCaptureCore calls back through the main run loop, so the main goroutine stays on the main thread.
func init() { runtime.LockOSThread() }

const (
	iccFindTimeout  = 6 * time.Second
	iccOpenTimeout  = 15 * time.Second
	iccCloseTimeout = 5 * time.Second
	// iccPendingTimeout is how long an open waits for the late confirmation of an earlier close of the
	// same device that timed out.
	iccPendingTimeout = 5 * time.Second
)

// iccCloseCheck runs the close bookkeeping checks in the shim (x_close_check); 0 means they passed.
func iccCloseCheck() int { return int(C.x_close_check()) }

type iccTransport struct {
	handle C.uintptr_t
	tid    uint32
}

func errNotMainThread() error {
	return errs.New(errs.General, "the macOS camera connection must be used from the main goroutine",
		"This is a bug in the tool; please report it.")
}

// Open connects to the first Fujifilm camera ImageCaptureCore knows. Call it, and use the transport,
// from the main goroutine.
func Open() (ptp.Transport, error) {
	var handle C.uintptr_t
	var message *C.char
	status := C.x_icc_open(C.double(iccFindTimeout.Seconds()), C.double(iccPendingTimeout.Seconds()),
		C.double(iccOpenTimeout.Seconds()), &handle, &message)
	reason := ""
	if message != nil {
		reason = C.GoString(message)
		C.free(unsafe.Pointer(message))
	}
	switch status {
	case C.XI_OK:
		return &iccTransport{handle: handle}, nil
	case C.XI_NOT_MAIN_THREAD:
		return nil, errNotMainThread()
	case C.XI_NOT_FOUND:
		if err := sandbox.Error(); err != nil {
			return nil, err
		}
		return nil, errs.New(errs.CameraNotFound, "no Fujifilm camera found",
			"Connect the camera with a data-capable USB-C cable and switch it on. If it is connected and "+
				"switched on, switch it off and on once: it may have stopped responding.")
	case C.XI_OPEN_TIMEOUT:
		return nil, errs.NotRespondingErr(fmt.Sprintf("macOS could not open the camera within %g s", iccOpenTimeout.Seconds()))
	case C.XI_CLOSE_PENDING:
		return nil, errs.New(errs.NotResponding, "the camera is still closing the previous connection",
			"Switch the camera off and on, then run the command again.")
	default: // XI_OPEN_ERROR
		return nil, errs.New(errs.CameraBusy, "macOS could not open the camera: "+reason,
			"Quit other apps that use the camera, then try again.")
	}
}

func (t *iccTransport) Transact(op uint16, params []uint32, dataOut []byte) (uint16, []uint32, []byte, error) {
	if op == ptp.OpOpenSession || op == ptp.OpCloseSession {
		return ptp.RCOK, nil, nil, nil // ptpcamerad owns the session
	}
	if t.handle == 0 {
		return 0, nil, nil, errs.NotRespondingErr("the camera connection is closed")
	}
	t.tid++
	command := commandContainer(op, t.tid, params)
	var out unsafe.Pointer
	if len(dataOut) > 0 {
		out = unsafe.Pointer(&dataOut[0])
	}
	limit := opTimeout(op)
	var reply C.x_reply
	status := C.x_icc_send(t.handle, unsafe.Pointer(&command[0]), C.size_t(len(command)), out,
		C.size_t(len(dataOut)), C.double(limit.Seconds()), &reply)
	defer func() {
		C.free(reply.data)
		C.free(reply.response)
		C.free(unsafe.Pointer(reply.message))
	}()
	switch status {
	case C.XI_OK:
	case C.XI_NOT_MAIN_THREAD:
		return 0, nil, nil, errNotMainThread()
	case C.XI_REMOVED:
		return 0, nil, nil, errs.NotRespondingErr("the camera was disconnected")
	case C.XI_TIMEOUT:
		return 0, nil, nil, errs.NotRespondingErr(
			fmt.Sprintf("the camera did not answer request 0x%04X within %g s", op, limit.Seconds()))
	default: // XI_ERROR
		return 0, nil, nil, errs.NotRespondingErr(
			fmt.Sprintf("macOS reported an error for request 0x%04X: %s", op, C.GoString(reply.message)))
	}
	data := C.GoBytes(reply.data, C.int(reply.dataLen))
	response := C.GoBytes(reply.response, C.int(reply.responseLen))
	h, ok := parseHeader(response)
	if !ok {
		return 0, nil, nil, errs.NotRespondingErr(fmt.Sprintf("the camera sent no response to request 0x%04X", op))
	}
	if h.ctype != ptp.Resp {
		return 0, nil, nil, errs.NotRespondingErr(fmt.Sprintf("unexpected reply type %d to request 0x%04X", h.ctype, op))
	}
	return h.code, responseParams(containerBody(response, h.length)), data, nil
}

// Close ends this app's session with the camera; the device browser stays for the next Open. It waits
// for macOS to confirm. The transport is closed whatever it returns: an error only reports that macOS
// did not confirm cleanly, which is no reason to fail a command whose work is done.
func (t *iccTransport) Close() error {
	if t.handle == 0 {
		return nil
	}
	var message *C.char
	status := C.x_icc_close(t.handle, C.double(iccCloseTimeout.Seconds()), &message)
	t.handle = 0
	reason := ""
	if message != nil {
		reason = C.GoString(message)
		C.free(unsafe.Pointer(message))
	}
	const hint = "Nothing needs doing now. If the next command cannot reach the camera, switch it off and on."
	switch status {
	case C.XI_OK:
		return nil
	case C.XI_NOT_MAIN_THREAD:
		return errNotMainThread()
	case C.XI_CLOSE_TIMEOUT:
		return errs.New(errs.NotResponding,
			fmt.Sprintf("macOS did not confirm closing the camera session within %g s", iccCloseTimeout.Seconds()), hint)
	default: // XI_ERROR
		return errs.New(errs.NotResponding, "macOS reported an error closing the camera session: "+reason, hint)
	}
}
