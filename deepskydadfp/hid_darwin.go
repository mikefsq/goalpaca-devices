//go:build darwin

package driver

// macOS HID transport over IOKit.

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/hid/IOHIDManager.h>
#include <IOKit/hid/IOHIDDevice.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

typedef struct {
    uint32_t loc;
    char serial[64];
    char product[64];
} sh_dev;

static int sh_int(IOHIDDeviceRef d, CFStringRef key) {
    int v = 0;
    CFTypeRef r = IOHIDDeviceGetProperty(d, key);
    if (r && CFGetTypeID(r) == CFNumberGetTypeID()) CFNumberGetValue((CFNumberRef)r, kCFNumberSInt32Type, &v);
    return v;
}

static void sh_str(IOHIDDeviceRef d, CFStringRef key, char *out, int cap) {
    out[0] = 0;
    CFTypeRef r = IOHIDDeviceGetProperty(d, key);
    if (r && CFGetTypeID(r) == CFStringGetTypeID()) CFStringGetCString((CFStringRef)r, out, cap, kCFStringEncodingUTF8);
}

// sh_copy_matching returns a retained array of matching devices; *n is set to its length.
static IOHIDDeviceRef *sh_copy_matching(int vid, int pid, int *n) {
    *n = 0;
    IOHIDManagerRef mgr = IOHIDManagerCreate(kCFAllocatorDefault, kIOHIDOptionsTypeNone);
    if (!mgr) return NULL;
    CFNumberRef v = CFNumberCreate(NULL, kCFNumberIntType, &vid);
    CFNumberRef p = CFNumberCreate(NULL, kCFNumberIntType, &pid);
    const void *keys[] = {CFSTR(kIOHIDVendorIDKey), CFSTR(kIOHIDProductIDKey)};
    const void *vals[] = {v, p};
    CFDictionaryRef match = CFDictionaryCreate(NULL, keys, vals, 2, &kCFTypeDictionaryKeyCallBacks,
                                               &kCFTypeDictionaryValueCallBacks);
    IOHIDManagerSetDeviceMatching(mgr, match);
    CFRelease(match); CFRelease(v); CFRelease(p);
    IOHIDManagerOpen(mgr, kIOHIDOptionsTypeNone);
    CFSetRef set = IOHIDManagerCopyDevices(mgr);
    IOHIDDeviceRef *out = NULL;
    if (set) {
        CFIndex c = CFSetGetCount(set);
        out = malloc(sizeof(IOHIDDeviceRef) * (c ? c : 1));
        CFSetGetValues(set, (const void **)out);
        for (CFIndex i = 0; i < c; i++) CFRetain(out[i]);
        *n = (int)c;
        CFRelease(set);
    }
    CFRelease(mgr);
    return out;
}

static int sh_list(int vid, int pid, sh_dev *devs, int max) {
    int n;
    IOHIDDeviceRef *all = sh_copy_matching(vid, pid, &n);
    int k = 0;
    for (int i = 0; i < n; i++) {
        if (k < max) {
            devs[k].loc = (uint32_t)sh_int(all[i], CFSTR(kIOHIDLocationIDKey));
            sh_str(all[i], CFSTR(kIOHIDSerialNumberKey), devs[k].serial, sizeof devs[k].serial);
            sh_str(all[i], CFSTR(kIOHIDProductKey), devs[k].product, sizeof devs[k].product);
            k++;
        }
        CFRelease(all[i]);
    }
    free(all);
    return k;
}

// sh_open opens the matching device at location loc. Returns NULL on failure.
static void *sh_open(int vid, int pid, uint32_t loc, int *rc) {
    int n;
    IOHIDDeviceRef *all = sh_copy_matching(vid, pid, &n);
    IOHIDDeviceRef chosen = NULL;
    for (int i = 0; i < n; i++) {
        if (!chosen && (uint32_t)sh_int(all[i], CFSTR(kIOHIDLocationIDKey)) == loc) chosen = all[i];
        else CFRelease(all[i]);
    }
    free(all);
    *rc = 0;
    if (!chosen) return NULL;
    IOReturn r = IOHIDDeviceOpen(chosen, kIOHIDOptionsTypeNone);
    if (r != kIOReturnSuccess) { *rc = r; CFRelease(chosen); return NULL; }
    return (void *)chosen;
}

static int sh_get(void *dev, uint8_t id, uint8_t *buf, long *len) {
    CFIndex l = *len;
    IOReturn r = IOHIDDeviceGetReport((IOHIDDeviceRef)dev, kIOHIDReportTypeFeature, id, buf, &l);
    *len = l;
    return r;
}

static int sh_set(void *dev, uint8_t id, uint8_t *buf, long len) {
    return IOHIDDeviceSetReport((IOHIDDeviceRef)dev, kIOHIDReportTypeFeature, id, buf, len);
}

static void sh_close(void *dev) {
    IOHIDDeviceClose((IOHIDDeviceRef)dev, kIOHIDOptionsTypeNone);
    CFRelease((IOHIDDeviceRef)dev);
}
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"
)

func listHID(vid, pid uint16) ([]hidDev, error) {
	var devs [16]C.sh_dev
	n := int(C.sh_list(C.int(vid), C.int(pid), &devs[0], C.int(len(devs))))
	out := make([]hidDev, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hidDev{
			path:    fmt.Sprintf("iokit:%08x", uint32(devs[i].loc)),
			serial:  C.GoString(&devs[i].serial[0]),
			product: C.GoString(&devs[i].product[0]),
		})
	}
	return out, nil
}

type darwinHID struct{ dev unsafe.Pointer }

func openHID(path string) (transport, error) {
	loc, err := strconv.ParseUint(strings.TrimPrefix(path, "iokit:"), 16, 32)
	if err != nil {
		return nil, fmt.Errorf("bad device path %q", path)
	}
	var rc C.int
	dev := C.sh_open(C.int(VID), C.int(PID), C.uint32_t(loc), &rc)
	if dev == nil {
		if rc != 0 {
			return nil, fmt.Errorf("IOHIDDeviceOpen %s: 0x%08x", path, uint32(rc))
		}
		return nil, fmt.Errorf("device %s is gone", path)
	}
	return &darwinHID{dev: dev}, nil
}

const getRetries = 3

// IOKit returns numbered reports with the report ID in byte 0.
func (h *darwinHID) getFeature(id byte, n int) ([]byte, error) {
	buf := make([]byte, n+1)
	var rc C.int
	l := C.long(len(buf))
	for try := 0; try < getRetries; try++ {
		l = C.long(len(buf))
		if rc = C.sh_get(h.dev, C.uint8_t(id), (*C.uint8_t)(unsafe.Pointer(&buf[0])), &l); rc == 0 {
			break
		}
	}
	if rc != 0 {
		return nil, fmt.Errorf("IOHIDDeviceGetReport(%d): 0x%08x", id, uint32(rc))
	}
	if int(l) < n+1 || buf[0] != id {
		return nil, fmt.Errorf("feature report %d: got %d bytes, want %d", id, int(l), n+1)
	}
	return buf[1:], nil
}

func (h *darwinHID) setFeature(id byte, data []byte) error {
	buf := append([]byte{id}, data...)
	if rc := C.sh_set(h.dev, C.uint8_t(id), (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.long(len(buf))); rc != 0 {
		return fmt.Errorf("IOHIDDeviceSetReport(%d): 0x%08x", id, uint32(rc))
	}
	return nil
}

func (h *darwinHID) close() error {
	C.sh_close(h.dev)
	return nil
}
