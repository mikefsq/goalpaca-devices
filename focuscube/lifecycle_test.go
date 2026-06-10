package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/pegasus-astro/focuscube"
)

// fakeDev builds a FocusCube over a scripted fakeSerial (defined in focuser_test.go).
func fakeDev(replies map[string]string) *focuscube.FocusCube {
	return focuscube.New(&fakeSerial{replies: replies}, focuscube.DeviceInfo{Port: "fake"})
}

// TestDisconnectedBehavior covers the dev==nil paths: every member returns the right
// not-connected/invalid error (or zero default) before a device is acquired.
func TestDisconnectedBehavior(t *testing.T) {
	f := NewPegasusFocuser(0, 50000)

	if f.Connected() {
		t.Error("Connected() = true before acquire")
	}
	if !f.Absolute() {
		t.Error("Absolute() = false, want true")
	}
	if f.MaxStep() != 50000 || f.MaxIncrement() != 50000 {
		t.Errorf("MaxStep/MaxIncrement = %d/%d, want 50000/50000", f.MaxStep(), f.MaxIncrement())
	}
	if f.IsMoving() {
		t.Error("IsMoving() = true when disconnected")
	}
	if f.Busy() {
		t.Error("Busy() = true when disconnected")
	}
	if _, err := f.Position(); err != alpacadev.ErrNotConnected {
		t.Errorf("Position() err = %v, want ErrNotConnected", err)
	}
	if _, err := f.Temperature(); err != alpacadev.ErrNotConnected {
		t.Errorf("Temperature() err = %v, want ErrNotConnected", err)
	}
	if err := f.Move(100); err != alpacadev.ErrNotConnected {
		t.Errorf("Move(100) err = %v, want ErrNotConnected", err)
	}
	if err := f.Move(-5); err != alpacadev.ErrInvalidValue {
		t.Errorf("Move(-5) err = %v, want ErrInvalidValue", err)
	}
	if err := f.Move(60000); err != alpacadev.ErrInvalidValue {
		t.Errorf("Move(over max) err = %v, want ErrInvalidValue", err)
	}
	if err := f.Halt(); err != alpacadev.ErrNotConnected {
		t.Errorf("Halt() err = %v, want ErrNotConnected", err)
	}
	if err := f.Connect(context.Background()); err != alpacadev.ErrNotConnected {
		t.Errorf("Connect() err = %v, want ErrNotConnected", err)
	}
	if err := f.Disconnect(context.Background()); err != nil {
		t.Errorf("Disconnect() err = %v, want nil", err)
	}
}

// TestCloseHaltsAndClears covers Close: it halts and closes the device and clears the
// handle so the driver reports disconnected.
func TestCloseHaltsAndClears(t *testing.T) {
	f := NewPegasusFocuser(0, 1000)
	f.dev = fakeDev(map[string]string{"#": "OK_FC", "H": "H:1"})
	if !f.Connected() {
		t.Fatal("expected connected with injected dev")
	}
	if err := f.Close(context.Background()); err != nil {
		t.Fatalf("Close() err = %v", err)
	}
	if f.Connected() {
		t.Error("still connected after Close()")
	}
}

// TestOpenAcquiresViaSupervisor covers Open -> Supervise -> manageHardware -> tryAcquire
// (the success path and the health-check loop) using an injected openDev.
func TestOpenAcquiresViaSupervisor(t *testing.T) {
	dev := fakeDev(map[string]string{"#": "OK_FC", "P": "4321", "I": "0", "T": "19.0"})
	f := NewPegasusFocuser(0, 80000)
	f.openDev = func() (*focuscube.FocusCube, error) { return dev, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.Open(ctx); err != nil {
		t.Fatal(err)
	}
	acquired := false
	for i := 0; i < 200; i++ { // up to ~2s for the supervisor to acquire
		if f.Connected() {
			acquired = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !acquired {
		t.Fatal("device never acquired via Open/manageHardware")
	}
	if p, err := f.Position(); err != nil || p != 4321 {
		t.Errorf("Position() = %d, %v; want 4321, nil", p, err)
	}
	if err := f.Connect(ctx); err != nil {
		t.Errorf("Connect() = %v once acquired, want nil", err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond) // let the supervisor observe ctx.Done and exit
}

// TestTryAcquireFailure covers tryAcquire's error branch (openDev fails -> stays down).
func TestTryAcquireFailure(t *testing.T) {
	f := NewPegasusFocuser(0, 1000)
	f.openDev = func() (*focuscube.FocusCube, error) { return nil, errors.New("no device") }
	if f.tryAcquire() {
		t.Error("tryAcquire() = true, want false when openDev errors")
	}
	if f.Connected() {
		t.Error("Connected() = true after a failed acquire")
	}
}

// TestOpenByIndexOutOfRange covers openByIndex's bounds check (index 99 is always past
// the enumerated count, so it errors without opening anything).
func TestOpenByIndexOutOfRange(t *testing.T) {
	f := NewPegasusFocuser(99, 1000)
	if _, err := f.openByIndex(); err == nil {
		t.Error("openByIndex(99) = nil err, want out-of-range")
	}
}

// TestIsMovingHandlesError covers IsMoving's error branch: a malformed "I" reply must
// not surface as moving=true.
func TestIsMovingHandlesError(t *testing.T) {
	f := NewPegasusFocuser(0, 1000)
	f.dev = fakeDev(map[string]string{"#": "OK_FC", "I": "notanint"})
	if f.IsMoving() {
		t.Error("IsMoving() = true on a parse error, want false")
	}
}

// TestSerialBinding covers NewPegasusFocuserBySerial: it builds the stable ID and wires
// the serial-based openDev. The acquire path is exercised with an injected device.
func TestSerialBinding(t *testing.T) {
	f := NewPegasusFocuserBySerial(0, "FT1ABCDE", 90000)
	if f.ID != "FocusCube-FT1ABCDE" {
		t.Errorf("ID = %q, want FocusCube-FT1ABCDE", f.ID)
	}
	if f.serial != "FT1ABCDE" {
		t.Errorf("serial = %q, want FT1ABCDE", f.serial)
	}
	if f.maxStep != 90000 {
		t.Errorf("maxStep = %d, want 90000", f.maxStep)
	}
	// Substitute the (real) serial resolver with a fake device and verify acquire.
	dev := fakeDev(map[string]string{"#": "OK_FC", "P": "10", "I": "0"})
	f.openDev = func() (*focuscube.FocusCube, error) { return dev, nil }
	if !f.tryAcquire() {
		t.Fatal("tryAcquire failed")
	}
	if !f.Connected() {
		t.Error("not connected after acquire")
	}
}

// TestOpenBySerialEmpty covers the openBySerial method surfacing the empty-serial error
// (which returns immediately, without enumerating ports).
func TestOpenBySerialEmpty(t *testing.T) {
	f := NewPegasusFocuserBySerial(0, "", 1000)
	if _, err := f.openBySerial(); err == nil {
		t.Error("openBySerial with empty serial = nil err, want error")
	}
}
