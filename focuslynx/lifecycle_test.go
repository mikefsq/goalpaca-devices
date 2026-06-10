package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/optec-astro/focuslynx"
)

// errHub is a focuslynx.Transport that answers every command with a controller error
// line ("ER=…") instead of the "!" ack, so queries fail fast (no reply timeout). It
// exercises the "connected but the query errors" branches.
type errHub struct{ sent bool }

func (e *errHub) Write(p []byte) (int, error) { e.sent = false; return len(p), nil }
func (e *errHub) Read(p []byte) (int, error) {
	if e.sent {
		return 0, nil
	}
	e.sent = true
	return copy(p, []byte("ER=3 Unknown Command Received\r\n")), nil
}
func (e *errHub) Close() error { return nil }

// TestDisconnectedBehavior covers the hub==nil paths: members return the right
// not-connected/invalid error (or zero default) before a hub is acquired.
func TestDisconnectedBehavior(t *testing.T) {
	f := NewOptecFocuser(0, 1)
	f.maxStep = 50000 // as cached at acquire

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
	if f.TempCompAvailable() {
		t.Error("TempCompAvailable() = true when disconnected")
	}
	if f.TempComp() {
		t.Error("TempComp() = true when disconnected")
	}
	if _, err := f.Position(); err != alpacadev.ErrNotConnected {
		t.Errorf("Position() err = %v, want ErrNotConnected", err)
	}
	if _, err := f.Temperature(); err != alpacadev.ErrNotConnected {
		t.Errorf("Temperature() err = %v, want ErrNotConnected", err)
	}
	if err := f.SetTempComp(true); err != alpacadev.ErrNotConnected {
		t.Errorf("SetTempComp() err = %v, want ErrNotConnected", err)
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

// TestConnectedButQueryFails covers the "hub present, query errors" branches: a
// controller error must not surface as moving/temp-comp true or a silent success.
func TestConnectedButQueryFails(t *testing.T) {
	f := NewOptecFocuser(0, 1)
	f.hub = focuslynx.New(&errHub{}, focuslynx.DeviceInfo{Port: "fake"})

	if f.IsMoving() {
		t.Error("IsMoving() = true on a query error")
	}
	if f.TempCompAvailable() {
		t.Error("TempCompAvailable() = true on a query error")
	}
	if f.TempComp() {
		t.Error("TempComp() = true on a query error")
	}
	if _, err := f.Position(); err == nil {
		t.Error("Position() err = nil on a query error, want non-nil")
	}
}

// TestCloseClears covers Close: it closes the hub and clears the handle.
func TestCloseClears(t *testing.T) {
	f := NewOptecFocuser(0, 1)
	f.hub = focuslynx.New(&fakeHub{pos: 1, maxStep: 1000}, focuslynx.DeviceInfo{Port: "fake"})
	if !f.Connected() {
		t.Fatal("expected connected with injected hub")
	}
	if err := f.Close(context.Background()); err != nil {
		t.Fatalf("Close() err = %v", err)
	}
	if f.Connected() {
		t.Error("still connected after Close()")
	}
}

// TestOpenAcquiresViaSupervisor covers Open -> Supervise -> manageHardware ->
// tryAcquire (success path + health-check loop) using an injected openDev.
func TestOpenAcquiresViaSupervisor(t *testing.T) {
	hub := focuslynx.New(&fakeHub{pos: 4321, maxStep: 80000}, focuslynx.DeviceInfo{Port: "fake"})
	f := NewOptecFocuser(0, 1)
	f.openDev = func() (*focuslynx.Hub, int, error) { return hub, 1, nil }

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
		t.Fatal("hub never acquired via Open/manageHardware")
	}
	if p, err := f.Position(); err != nil || p != 4321 {
		t.Errorf("Position() = %d, %v; want 4321, nil", p, err)
	}
	if f.MaxStep() != 80000 {
		t.Errorf("MaxStep() = %d, want 80000 (cached at acquire)", f.MaxStep())
	}
	if err := f.Connect(ctx); err != nil {
		t.Errorf("Connect() = %v once acquired, want nil", err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond) // let the supervisor observe ctx.Done and exit
}

// TestTryAcquireFailure covers tryAcquire's error branch (openDev fails -> stays down).
func TestTryAcquireFailure(t *testing.T) {
	f := NewOptecFocuser(0, 1)
	f.openDev = func() (*focuslynx.Hub, int, error) { return nil, 0, errors.New("no hub") }
	if f.tryAcquire() {
		t.Error("tryAcquire() = true, want false when openDev errors")
	}
	if f.Connected() {
		t.Error("Connected() = true after a failed acquire")
	}
}

// TestOpenByIndexOutOfRange covers openByIndex's bounds check (index 99 is always past
// the enumerated count, so it errors without opening a port).
func TestOpenByIndexOutOfRange(t *testing.T) {
	f := NewOptecFocuser(99, 1)
	if _, _, err := f.openByIndex(); err == nil {
		t.Error("openByIndex(99) = nil err, want out-of-range")
	}
}

// TestOpenByNicknameEmpty covers openByNickname surfacing the empty-nickname error
// (which returns immediately, without scanning ports).
func TestOpenByNicknameEmpty(t *testing.T) {
	f := NewOptecFocuserByNickname(0, "")
	if _, _, err := f.openByNickname(); err == nil {
		t.Error("openByNickname with empty nickname = nil err, want error")
	}
}
