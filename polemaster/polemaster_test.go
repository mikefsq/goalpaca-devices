package driver

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/polemaster"
)

// Check representative subframes, including odd origins and sensor edges.
// Each enclosing window must preserve coverage and satisfy the USB library.
func TestHardwareWindowCovers(t *testing.T) {
	for _, tc := range []struct{ x, y, w, h int }{
		{0, 0, polemaster.Width, polemaster.Height},
		{0, 0, 1, 1},
		{1, 1, 1, 1},
		{0, 0, 640, 480},
		{320, 240, 640, 480},
		{321, 241, 639, 479},
		{0, 0, 100, 100},
		{1279, 959, 1, 1},
		{640, 480, 640, 480},
		{0, 0, 1280, 2},
		{0, 0, 2, 960},
		{7, 13, 101, 203},
		{1000, 800, 200, 100},
	} {
		x, y, w, h := hardwareWindow(tc.x, tc.y, tc.w, tc.h)
		if x%2 != 0 || y%2 != 0 || w%2 != 0 || h%2 != 0 {
			t.Errorf("%dx%d+%d+%d gave an odd window %dx%d+%d+%d",
				tc.w, tc.h, tc.x, tc.y, w, h, x, y)
		}
		if (w*h)%packetPixels != 0 {
			t.Errorf("%dx%d+%d+%d gave %dx%d, %d pixels, not a whole number of packets",
				tc.w, tc.h, tc.x, tc.y, w, h, w*h)
		}
		if x+w > polemaster.Width || y+h > polemaster.Height {
			t.Errorf("%dx%d+%d+%d gave %dx%d+%d+%d, outside the array",
				tc.w, tc.h, tc.x, tc.y, w, h, x, y)
		}
		if x > tc.x || y > tc.y || x+w < tc.x+tc.w || y+h < tc.y+tc.h {
			t.Errorf("%dx%d+%d+%d is not covered by %dx%d+%d+%d",
				tc.w, tc.h, tc.x, tc.y, w, h, x, y)
		}
		// The library has to accept whatever the search returns.
		if err := polemaster.Attach(newFake()).SetROI(x, y, w, h); err != nil {
			t.Errorf("%dx%d+%d+%d gave a window the camera rejects: %v", tc.w, tc.h, tc.x, tc.y, err)
		}
	}
}

// TestHardwareWindowRejects checks that a subframe outside the array falls back
// to the full frame rather than to a window the sensor would refuse.
func TestHardwareWindowRejects(t *testing.T) {
	for _, tc := range []struct{ x, y, w, h int }{
		{0, 0, 0, 100},
		{0, 0, 1281, 100},
		{1280, 0, 1, 1},
		{-1, 0, 10, 10},
	} {
		x, y, w, h := hardwareWindow(tc.x, tc.y, tc.w, tc.h)
		if x != 0 || y != 0 || w != polemaster.Width || h != polemaster.Height {
			t.Errorf("%dx%d+%d+%d gave %dx%d+%d+%d, want the full frame",
				tc.w, tc.h, tc.x, tc.y, w, h, x, y)
		}
	}
}

// Prefer extra columns to extra rows because height determines readout time.
func TestHardwareWindowIsNotWastefullyTall(t *testing.T) {
	for _, tc := range []struct{ x, y, w, h int }{
		{0, 0, 100, 100},
		{0, 0, 640, 480},
		{100, 100, 300, 200},
	} {
		_, _, _, h := hardwareWindow(tc.x, tc.y, tc.w, tc.h)
		if h != even(tc.h) {
			t.Errorf("%dx%d grew to %d rows, want %d", tc.w, tc.h, h, even(tc.h))
		}
	}
}

func TestCrop(t *testing.T) {
	// A 4x3 window of one-byte pixels, each carrying its own index.
	src := []byte{
		0, 1, 2, 3,
		4, 5, 6, 7,
		8, 9, 10, 11,
	}
	got := crop(src, 4, 3, 1, 1, 1, 2, 2)
	want := []byte{5, 6, 9, 10}
	if string(got) != string(want) {
		t.Errorf("crop gave % d, want % d", got, want)
	}
	// A whole-window crop returns the input untouched.
	if same := crop(src, 4, 3, 1, 0, 0, 4, 3); &same[0] != &src[0] {
		t.Error("a full-window crop copied when it did not need to")
	}
}

func TestDecode12(t *testing.T) {
	// Wire format: the first byte holds bits 11:4, the second holds bits 3:0
	// beneath a nibble of ones from the unconnected bridge inputs.
	for _, v := range []uint16{0, 1, 0x123, 0x0abc, 0x0f0f, 4095} {
		wire := []byte{byte(v >> 4), byte(v&0x0f) | 0xf0}
		out := decode12(wire)
		if got := binary.LittleEndian.Uint16(out); got != v {
			t.Errorf("decode12 of % x gave %d, want %d", wire, got, v)
		}
	}
}

// Exercise capture through ImageFrame with coordinate-derived simulated pixels.
func TestExposureCycle(t *testing.T) {
	f := newFake()
	p := newTestDriver(t, f)
	defer p.Close(context.Background())

	const sx, sy, w, h = 101, 203, 50, 40
	mustSet(t, p.SetStartX(sx))
	mustSet(t, p.SetStartY(sy))
	mustSet(t, p.SetNumX(w))
	mustSet(t, p.SetNumY(h))

	if err := p.StartExposure(0.001, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	waitReady(t, p)

	fr, err := p.ImageFrame()
	if err != nil {
		t.Fatalf("ImageFrame: %v", err)
	}
	if fr.Width != w || fr.Height != h {
		t.Fatalf("frame is %dx%d, want %dx%d", fr.Width, fr.Height, w, h)
	}
	if len(fr.Pixels) != w*h {
		t.Fatalf("frame carries %d bytes, want %d", len(fr.Pixels), w*h)
	}
	if fr.TransmissionElementType != alpacadev.ImgByte {
		t.Errorf("eight-bit frame is transmitted as %v, want a byte", fr.TransmissionElementType)
	}
	// Coordinate-derived pixels detect crops taken from the wrong sensor region.
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			want := pattern8(sx+col, sy+row)
			if got := fr.Pixels[row*w+col]; got != want {
				t.Fatalf("pixel (%d,%d) is %d, want %d (the crop is offset)",
					col, row, got, want)
			}
		}
	}
}

// TestExposureCycle12 checks the twelve-bit path, where the wire format is not
// a 16-bit word and has to be decoded before it goes out.
func TestExposureCycle12(t *testing.T) {
	f := newFake()
	p := newTestDriver(t, f)
	defer p.Close(context.Background())

	if err := p.SetReadoutMode(1); err != nil {
		t.Fatalf("SetReadoutMode: %v", err)
	}
	if p.MaxADU() != 4095 {
		t.Errorf("MaxADU is %d at twelve bits, want 4095", p.MaxADU())
	}
	const sx, sy, w, h = 8, 6, 16, 4
	mustSet(t, p.SetStartX(sx))
	mustSet(t, p.SetStartY(sy))
	mustSet(t, p.SetNumX(w))
	mustSet(t, p.SetNumY(h))

	if err := p.StartExposure(0.001, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	waitReady(t, p)

	fr, err := p.ImageFrame()
	if err != nil {
		t.Fatalf("ImageFrame: %v", err)
	}
	if fr.TransmissionElementType != alpacadev.ImgUInt16 {
		t.Errorf("twelve-bit frame is transmitted as %v, want a 16-bit word",
			fr.TransmissionElementType)
	}
	if len(fr.Pixels) != w*h*2 {
		t.Fatalf("frame carries %d bytes, want %d", len(fr.Pixels), w*h*2)
	}
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			want := pattern12(sx+col, sy+row)
			got := binary.LittleEndian.Uint16(fr.Pixels[2*(row*w+col):])
			if got != want {
				t.Fatalf("pixel (%d,%d) is %d, want %d", col, row, got, want)
			}
			if got > 4095 {
				t.Fatalf("pixel (%d,%d) is %d, past twelve bits", col, row, got)
			}
		}
	}
}

// No image is available before the first completed capture.
func TestImageFrameNotSetBeforeExposure(t *testing.T) {
	p := newTestDriver(t, newFake())
	defer p.Close(context.Background())
	if _, err := p.ImageFrame(); err == nil {
		t.Fatal("ImageFrame returned a frame before any exposure")
	}
	if p.ImageReady() {
		t.Error("ImageReady is true before any exposure")
	}
	if p.CameraState() != alpacadev.CameraIdle {
		t.Errorf("CameraState is %v before any exposure, want idle", p.CameraState())
	}
}

func TestSettersRejectOutOfRange(t *testing.T) {
	p := newTestDriver(t, newFake())
	defer p.Close(context.Background())
	for name, err := range map[string]error{
		"gain 0":            p.SetGain(polemaster.GainMin - 1),
		"gain 41":           p.SetGain(polemaster.GainMax + 1),
		"offset -1":         p.SetOffset(polemaster.OffsetMin - 1),
		"offset 4046":       p.SetOffset(polemaster.OffsetMax + 1),
		"readout mode 2":    p.SetReadoutMode(len(readoutModes)),
		"readout mode -1":   p.SetReadoutMode(-1),
		"startx 1280":       p.SetStartX(polemaster.Width),
		"numx 0":            p.SetNumX(0),
		"numy 961":          p.SetNumY(polemaster.Height + 1),
		"exposure -1":       p.StartExposure(-1, true),
		"exposure too long": p.StartExposure(p.ExposureMax()*2, true),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCapabilities(t *testing.T) {
	p := NewPoleMaster("")
	if p.CameraXSize() != polemaster.Width || p.CameraYSize() != polemaster.Height {
		t.Errorf("sensor is %dx%d", p.CameraXSize(), p.CameraYSize())
	}
	if p.MaxBinX() != 1 || p.MaxBinY() != 1 {
		t.Error("the camera advertises binning it does not have")
	}
	if p.HasShutter() {
		t.Error("the camera advertises a shutter it does not have")
	}
	if p.CanSetCCDTemperature() || p.CanGetCoolerPower() || p.CanPulseGuide() {
		t.Error("the camera advertises cooling or guiding it does not have")
	}
	if !p.CanAbortExposure() || p.CanStopExposure() {
		t.Error("abort and stop are the wrong way round")
	}
	if got := p.ExposureMin(); got != polemaster.RowTime.Seconds() {
		t.Errorf("ExposureMin is %v, want one row time", got)
	}
	if p.SensorType() != alpacadev.SensorMonochrome {
		t.Error("the sensor is monochrome")
	}
	if _, err := p.BayerOffsetX(); err == nil {
		t.Error("a monochrome sensor answered BayerOffsetX")
	}
}

// Helpers.

func newTestDriver(t *testing.T, f *fakeDevice) *PoleMaster {
	t.Helper()
	p := NewPoleMaster("")
	p.openDev = func() (*polemaster.Camera, error) {
		c := polemaster.Attach(f)
		if err := c.Init(); err != nil {
			return nil, err
		}
		return c, nil
	}
	p.tryAcquire()
	if !p.Connected() {
		t.Fatal("the fake camera did not come up")
	}
	return p
}

func mustSet(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitReady(t *testing.T, p *PoleMaster) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !p.ImageReady() {
		if p.CameraState() == alpacadev.CameraError {
			t.Fatal("the exposure failed")
		}
		if time.Now().After(deadline) {
			t.Fatal("the exposure did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

// pattern8 and pattern12 give each sensor pixel a value derived from its
// absolute position, so a crop that lands in the wrong place is visible.
func pattern8(x, y int) byte    { return byte(x*7 + y*13) }
func pattern12(x, y int) uint16 { return uint16((x*7 + y*13) & 0x0fff) }

// fakeDevice is a polemaster.Device that answers register traffic and streams
// frames whose geometry follows the window the driver programmed.
type fakeDevice struct {
	closes int
	regs   map[uint16]uint16
	wide   bool   // two bytes per pixel
	buf    []byte // the pending wire stream
}

// The wire constants the fake has to understand, as they appear on the bus.
const (
	reqI2CWrite = 0xbb
	reqI2CRead  = 0xb7
	reqTransfer = 0xcd
	regXStart   = 0x3004
	regXEnd     = 0x3008
	regYStart   = 0x3002
	regYEnd     = 0x3006
	regChipVer  = 0x3000
	chipMT9M034 = 0x2400
	addrOrigin  = 4
)

func newFake() *fakeDevice {
	return &fakeDevice{regs: map[uint16]uint16{regChipVer: chipMT9M034}}
}

func (f *fakeDevice) PID() uint16 { return polemaster.PIDCamera }

func (f *fakeDevice) ControlOut(req uint8, val, idx uint16, data []byte) error {
	switch req {
	case reqI2CWrite:
		f.regs[idx] = uint16(data[0])<<8 | uint16(data[1])
	case reqTransfer:
		f.wide = data[0] != 0
	}
	return nil
}

func (f *fakeDevice) ControlIn(req uint8, val, idx uint16, data []byte) (int, error) {
	if req == reqI2CRead {
		v := f.regs[idx]
		data[0], data[1] = byte(v>>8), byte(v)
		return 2, nil
	}
	for i := range data {
		data[i] = 0
	}
	return len(data), nil
}

// window reports the readout window the driver has programmed.
func (f *fakeDevice) window() (x, y, w, h int) {
	x = int(f.regs[regXStart]) - addrOrigin
	y = int(f.regs[regYStart]) - addrOrigin
	w = int(f.regs[regXEnd]) - int(f.regs[regXStart]) + 1
	h = int(f.regs[regYEnd]) - int(f.regs[regYStart]) + 1
	return
}

// frame builds one wire frame: the window's pixels, then the four-byte marker
// and the stale fifth byte the bridge commits with it.
func (f *fakeDevice) frame() []byte {
	x, y, w, h := f.window()
	bpp := 1
	if f.wide {
		bpp = 2
	}
	out := make([]byte, 0, w*h*bpp+5)
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			if f.wide {
				v := pattern12(x+col, y+row)
				out = append(out, byte(v>>4), byte(v&0x0f)|0xf0)
			} else {
				out = append(out, pattern8(x+col, y+row))
			}
		}
	}
	return append(out, 0xaa, 0x11, 0xcc, 0xee, 0x00)
}

func (f *fakeDevice) BulkRead(buf []byte, _ time.Duration) (int, error) {
	for len(f.buf) < len(buf) {
		f.buf = append(f.buf, f.frame()...)
	}
	n := copy(buf, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *fakeDevice) ClearHalt() error { f.buf = nil; return nil }
func (f *fakeDevice) Reset() error     { return nil }
func (f *fakeDevice) Close() error     { f.closes++; return nil }

func TestReacquireClosesOldHandle(t *testing.T) {
	f := newFake()
	p := newTestDriver(t, f)
	defer p.Close(context.Background())
	p.hwPresent.Store(false)
	p.tryAcquire()
	if f.closes != 1 {
		t.Fatalf("old handle closed %d times, want once", f.closes)
	}
	if !p.Connected() {
		t.Fatal("camera was not reacquired")
	}
}
