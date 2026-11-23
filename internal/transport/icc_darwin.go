//go:build darwin && cgo

package transport

// macOS: PTP through ImageCaptureCore, the way FUJIFILM X RAW STUDIO for macOS talks to the camera.
//
// ptpcamerad owns every PTP camera and keeps the PTP session open. ICCameraDevice
// requestSendPTPCommand:outData:completion: takes a complete PTP command container plus the raw bytes of
// an outgoing data phase, and hands back the incoming data and the response container. Nothing has to be
// stopped, and other apps can stay connected.
//
// ImageCaptureCore delivers its callbacks through the main run loop, which the shim below spins while it
// waits. So the transport must be used from the main goroutine: init locks it to the main OS thread, and
// a call from any other thread fails with an error instead of waiting for callbacks that never come.

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
};

typedef struct {
	void *data;
	size_t dataLen;
	void *response;
	size_t responseLen;
	char *message;
} x_reply;

@interface X100VIGoICCDelegate : NSObject <ICDeviceBrowserDelegate, ICCameraDeviceDelegate>
@property (atomic, strong) NSMutableArray *devices;
@property (atomic) BOOL opened;
@property (atomic, strong) NSError *openError;
@property (atomic) BOOL removed;
@end

@implementation X100VIGoICCDelegate
- (instancetype)init {
	self = [super init];
	if (self) {
		_devices = [NSMutableArray array];
	}
	return self;
}
// ICDeviceBrowserDelegate
- (void)deviceBrowser:(ICDeviceBrowser *)browser didAddDevice:(ICDevice *)device moreComing:(BOOL)more {
	@synchronized (self) { [self.devices addObject:device]; }
}
- (void)deviceBrowser:(ICDeviceBrowser *)browser didRemoveDevice:(ICDevice *)device moreGoing:(BOOL)more {
	@synchronized (self) { [self.devices removeObject:device]; }
}
// ICDeviceDelegate
- (void)device:(ICDevice *)device didOpenSessionWithError:(NSError *)error {
	self.openError = error;
	self.opened = YES;
}
- (void)device:(ICDevice *)device didCloseSessionWithError:(NSError *)error {}
- (void)didRemoveDevice:(ICDevice *)device { self.removed = YES; }
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

static uint64_t x_now(void) { return clock_gettime_nsec_np(CLOCK_UPTIME_RAW); }

// x_spin runs the current thread's run loop until done() or the timeout.
static BOOL x_spin(BOOL (^done)(void), double timeout) {
	uint64_t end = x_now() + (uint64_t)(timeout * 1e9);
	while (!done()) {
		if (x_now() >= end) {
			return NO;
		}
		@autoreleasepool {
			if (CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.02, false) == kCFRunLoopRunFinished) {
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

static int x_icc_open(double findTimeout, double openTimeout, uintptr_t *handle, char **message) {
	@autoreleasepool {
		*handle = 0;
		*message = NULL;
		if (!pthread_main_np()) {
			return XI_NOT_MAIN_THREAD;
		}
		if (gBrowser == nil) {
			gDelegate = [[X100VIGoICCDelegate alloc] init];
			gBrowser = [[ICDeviceBrowser alloc] init];
			gBrowser.delegate = gDelegate;
			// Cameras, at every location (ICDeviceLocationTypeMaskAll).
			gBrowser.browsedDeviceTypeMask = (ICDeviceTypeMask)(ICDeviceTypeMaskCamera | 0x0000FF00);
			[gBrowser start];
		}
		gDelegate.opened = NO;
		gDelegate.openError = nil;
		gDelegate.removed = NO;
		x_spin(^BOOL { return x_find() != nil; }, findTimeout);
		ICCameraDevice *camera = x_find();
		if (camera == nil) {
			return XI_NOT_FOUND;
		}
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
		if (gDelegate.removed) {
			return XI_REMOVED;
		}
		ICCameraDevice *camera = (__bridge ICCameraDevice *)(void *)handle;
		X100VIGoICCReply *box = [[X100VIGoICCReply alloc] init];
		NSData *cmd = [NSData dataWithBytes:command length:commandLen];
		NSData *out = outLen ? [NSData dataWithBytes:outData length:outLen] : [NSData data];
		[camera requestSendPTPCommand:cmd outData:out completion:^(NSData *data, NSData *response, NSError *error) {
			box.data = data;
			box.response = response;
			box.error = error;
			box.done = YES;
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

// x_icc_close ends this app's session but keeps the browser.
static void x_icc_close(uintptr_t handle) {
	@autoreleasepool {
		ICCameraDevice *camera = CFBridgingRelease((void *)handle);
		[camera requestCloseSession];
		x_spin(^BOOL { return NO; }, 0.2);
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
	iccFindTimeout = 6 * time.Second
	iccOpenTimeout = 15 * time.Second
)

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
	status := C.x_icc_open(C.double(iccFindTimeout.Seconds()), C.double(iccOpenTimeout.Seconds()), &handle, &message)
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

// Close ends this app's session with the camera; the device browser stays for the next Open.
func (t *iccTransport) Close() error {
	if t.handle != 0 {
		C.x_icc_close(t.handle)
		t.handle = 0
	}
	return nil
}
