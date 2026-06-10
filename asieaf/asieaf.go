package driver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/eaf"
)

// Compile-time check that the driver satisfies the Alpaca Focuser interface.
var _ alpacadev.Focuser = (*ASIFocuser)(nil)

// ASIFocuser adapts a goasi/eaf focuser to the alpacadev.Focuser + Hardware
// interfaces. It is the device-specific code: it knows about goasi/ZWO; the
// library does not.
//
// The pure-Go eaf driver serializes per device internally, but the open handle
// (f.dev) is shared with the hardware-management goroutine, so mu guards the
// handle pointer + cached props. mu is held across eaf calls (matching asiefw);
// it is never held across the manager's own sleeps.
type ASIFocuser struct {
	alpacadev.BaseFocuser

	index int

	mu  sync.Mutex
	dev *eaf.EAF // open handle; nil when no focuser is attached

	maxStep int // cached at acquire (device-reported; EAF is geared, no clutch)

	// openDev opens the target focuser; set once at construction. Defaults to
	// openByIndex; tests inject a fake-backed handle.
	openDev func() (*eaf.EAF, error)
}

// EAF is an absolute focuser, so MaxIncrement equals the full travel.
func (f *ASIFocuser) Absolute() bool    { return true }
func (f *ASIFocuser) MaxStep() int      { return f.maxStep }
func (f *ASIFocuser) MaxIncrement() int { return f.maxStep }

// NewASIFocuser creates the driver for the focuser at the given enumeration index.
// (Serial binding awaits an eaf.SerialNumber decode; for now selection is by index,
// so the serial arg is accepted for CLI compatibility but only labels the device.)
func NewASIFocuser(index int, serial string) *ASIFocuser {
	f := &ASIFocuser{index: index}
	f.Version = "0.2.0"
	f.Info = "asieaf — ZWO EAF Alpaca driver over pure-Go goasi/eaf"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = fmt.Sprintf("EAF-foc%d", index)
	f.DevName = fmt.Sprintf("ASI EAF %d", index)
	f.openDev = f.openByIndex
	return f
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca server comes up with or without a focuser attached.
func (f *ASIFocuser) Open(ctx context.Context) error {
	if f.openDev == nil {
		f.openDev = f.openByIndex
	}
	go alpacadev.Supervise(ctx, f.ID, func() { f.manageHardware(ctx) })
	return nil
}

// Close releases the handle on graceful shutdown only.
func (f *ASIFocuser) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev != nil {
		f.dev.Stop()
		f.dev.Close()
		f.dev = nil
	}
	return nil
}

// Connect is the client's presence handshake: it succeeds iff the focuser is
// attached (Connected ≡ handle open). It does not open hardware — the driver
// already owns it.
func (f *ASIFocuser) Connect(ctx context.Context) error {
	if !f.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports hardware presence: connected exactly when the handle is open.
func (f *ASIFocuser) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dev != nil
}

// Disconnect is a logical no-op: the driver owns the hardware for the life of the
// process; connection state follows the hardware, not client sessions.
func (f *ASIFocuser) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes while the focuser is moving.
func (f *ASIFocuser) Busy() bool { return f.IsMoving() }

// manageHardware acquires, monitors, and re-acquires the focuser for the life of
// the process.
func (f *ASIFocuser) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		present := f.dev != nil
		f.mu.Unlock()

		if !present {
			if f.tryAcquire() {
				log.Printf("asieaf: focuser %s acquired", f.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}

		// Monitor: a healthy focuser reports its position without error.
		f.mu.Lock()
		var err error
		if f.dev != nil {
			_, err = f.dev.Position()
		}
		if err != nil {
			log.Printf("asieaf: focuser %s lost (%v); re-acquiring", f.ID, err)
			f.dev.Close()
			f.dev = nil // Connected() follows this; gate returns NotConnected
		}
		f.mu.Unlock()
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire opens the target focuser and caches its properties. Returns true once
// the focuser is open and ready.
func (f *ASIFocuser) tryAcquire() bool {
	dev, err := f.openDev()
	if err != nil {
		return false
	}
	f.configureOpened(dev)
	return true
}

// openByIndex opens the focuser at the configured enumeration index.
func (f *ASIFocuser) openByIndex() (*eaf.EAF, error) {
	devs, err := eaf.Enumerate()
	if err != nil {
		return nil, err
	}
	if f.index < 0 || f.index >= len(devs) {
		return nil, fmt.Errorf("no EAF at index %d (found %d)", f.index, len(devs))
	}
	return eaf.OpenAt(devs[f.index].LocationID)
}

// configureOpened caches a freshly opened focuser's properties and publishes it.
func (f *ASIFocuser) configureOpened(dev *eaf.EAF) {
	maxStep, _ := dev.MaxStep()
	maj, min := dev.FirmwareVersion()

	f.mu.Lock()
	defer f.mu.Unlock()
	f.dev = dev
	if maxStep > 0 {
		f.maxStep = maxStep
	}
	f.Desc = fmt.Sprintf("ZWO EAF (max %d steps, fw %d.%d)", f.maxStep, maj, min)
}

// --- Focuser members ---

func (f *ASIFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return false
	}
	moving, err := f.dev.IsMoving()
	return err == nil && moving
}

func (f *ASIFocuser) Position() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.dev.Position()
}

// Temperature is intentionally not overridden: the EAF reports a raw thermistor
// value whose °C conversion (lookup table) is not yet decoded, so BaseFocuser's
// default (ErrNotImplemented) applies rather than reporting wrong units.

// Move drives the absolute focuser to the given step. Returns once the move is
// initiated; clients poll IsMoving for completion.
func (f *ASIFocuser) Move(position int) error {
	if position < 0 || (f.maxStep > 0 && position > f.maxStep) {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.MoveTo(position)
}

func (f *ASIFocuser) Halt() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.Stop()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
