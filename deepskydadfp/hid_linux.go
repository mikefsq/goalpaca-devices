//go:build linux

package driver

// Linux HID transport: hidraw feature-report ioctls.
// Non-root access needs the udev rule in the driver README.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const hidrawSysfs = "/sys/class/hidraw"

// iocRW encodes a read/write ioctl of type 'H' (asm-generic layout).
func iocRW(nr, size uintptr) uintptr {
	const dir = 3 // _IOC_READ | _IOC_WRITE
	return (dir << 30) | (size << 16) | (uintptr('H') << 8) | nr
}

func hidiocSFeature(n int) uintptr { return iocRW(0x06, uintptr(n)) }
func hidiocGFeature(n int) uintptr { return iocRW(0x07, uintptr(n)) }

// readUevent returns the KEY=value pairs of a sysfs uevent file.
func readUevent(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			m[k] = v
		}
	}
	return m
}

// parseHIDID reads "0003:00001209:00000001" (bus:vid:pid).
func parseHIDID(s string) (vid, pid uint16, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, 0, false
	}
	v, e1 := strconv.ParseUint(parts[1], 16, 32)
	p, e2 := strconv.ParseUint(parts[2], 16, 32)
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	return uint16(v), uint16(p), true
}

func listHID(vid, pid uint16) ([]hidDev, error) {
	entries, err := os.ReadDir(hidrawSysfs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", hidrawSysfs, err)
	}
	var out []hidDev
	for _, e := range entries {
		ue := readUevent(filepath.Join(hidrawSysfs, e.Name(), "device", "uevent"))
		v, p, ok := parseHIDID(ue["HID_ID"])
		if !ok || v != vid || p != pid {
			continue
		}
		out = append(out, hidDev{path: "/dev/" + e.Name(), serial: ue["HID_UNIQ"], product: ue["HID_NAME"]})
	}
	return out, nil
}

type linuxHID struct{ f *os.File }

func openHID(path string) (transport, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (non-root access needs a udev rule, see the driver README)", path, err)
	}
	return &linuxHID{f: f}, nil
}

func (h *linuxHID) ioctl(req uintptr, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, errors.New("empty buffer")
	}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, h.f.Fd(), req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// hidraw returns numbered reports with the report ID in byte 0.
func (h *linuxHID) getFeature(id byte, n int) ([]byte, error) {
	buf := make([]byte, n+1)
	buf[0] = id
	got, err := h.ioctl(hidiocGFeature(len(buf)), buf)
	if err != nil {
		return nil, fmt.Errorf("HIDIOCGFEATURE(%d): %w", id, err)
	}
	if got < n+1 {
		return nil, fmt.Errorf("feature report %d: got %d bytes, want %d", id, got, n+1)
	}
	return buf[1:], nil
}

func (h *linuxHID) setFeature(id byte, data []byte) error {
	buf := append([]byte{id}, data...)
	if _, err := h.ioctl(hidiocSFeature(len(buf)), buf); err != nil {
		return fmt.Errorf("HIDIOCSFEATURE(%d): %w", id, err)
	}
	return nil
}

func (h *linuxHID) close() error { return h.f.Close() }
