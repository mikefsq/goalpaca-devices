package driver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfoc"
)

var _ alpacadev.Focuser = (*OasisFocuser)(nil)

// OasisFocuser adapts an oasis-astro/oasisfoc focuser to the alpacadev.Focuser +
// Hardware interfaces. It is the device-specific code; the oasisfoc library knows
// nothing about Alpaca.
//
// The focuser has an absolute encoder (MoveTo/Position), so it presents as an ASCOM
// absolute focuser; it also has a manual clutch, so the reported position can
// change without a commanded move — that is fine, the encoder still tracks it.
type OasisFocuser struct {
	alpacadev.BaseFocuser

	index int

	mu  sync.Mutex
	dev *oasisfoc.Oasis // open handle; nil when no focuser is attached

	maxStep int // device-reported travel, cached at acquire

	// openDev opens the target focuser; set once at construction. Tests inject a
	// fake-transport-backed handle.
	openDev func() (*oasisfoc.Oasis, error)
}

// NewOasisFocuser creates the driver for the focuser at the given enumeration index.
func NewOasisFocuser(index int) *OasisFocuser {
	f := &OasisFocuser{index: index}
	f.Version = "0.1.0"
	f.Info = "oasisfoc — Astroasis Oasis focuser Alpaca driver over pure-Go oasis-astro/oasisfoc"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = fmt.Sprintf("Oasis-foc%d", index)
	f.DevName = fmt.Sprintf("Oasis Focuser %d", index)
	f.openDev = f.openByIndex
	return f
}

// Absolute focuser: MaxIncrement equals the full travel.
func (f *OasisFocuser) Absolute() bool    { return true }
func (f *OasisFocuser) MaxStep() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }
func (f *OasisFocuser) MaxIncrement() int { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }

// --- Hardware lifecycle (persistent owner) ---

func (f *OasisFocuser) Open(ctx context.Context) error {
	if f.openDev == nil {
		f.openDev = f.openByIndex
	}
	go alpacadev.Supervise(ctx, f.ID, func() { f.manageHardware(ctx) })
	return nil
}

func (f *OasisFocuser) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev != nil {
		f.dev.StopMove()
		f.dev.Close()
		f.dev = nil
	}
	return nil
}

func (f *OasisFocuser) Connect(ctx context.Context) error {
	if !f.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (f *OasisFocuser) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dev != nil
}

func (f *OasisFocuser) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes while the focuser is moving.
func (f *OasisFocuser) Busy() bool { return f.IsMoving() }

func (f *OasisFocuser) manageHardware(ctx context.Context) {
	// Release the device handle when the supervised loop ends (ctx cancelled). Without this
	// the HID handle leaks on shutdown — on macOS/IOKit that can leave the device wedged
	// (unenumerable) until a replug.
	defer func() {
		f.mu.Lock()
		if f.dev != nil {
			f.dev.StopMove()
			f.dev.Close()
			f.dev = nil
		}
		f.mu.Unlock()
	}()
	for ctx.Err() == nil {
		f.mu.Lock()
		present := f.dev != nil
		f.mu.Unlock()
		if !present {
			if f.tryAcquire() {
				log.Printf("oasisfoc: focuser %s acquired", f.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		f.mu.Lock()
		var err error
		if f.dev != nil {
			_, err = f.dev.Position()
		}
		if err != nil {
			log.Printf("oasisfoc: focuser %s lost (%v); re-acquiring", f.ID, err)
			f.dev.Close()
			f.dev = nil
		}
		f.mu.Unlock()
		sleepCtx(ctx, 2*time.Second)
	}
}

func (f *OasisFocuser) tryAcquire() bool {
	dev, err := f.openDev()
	if err != nil {
		return false
	}
	maxStep, _ := dev.MaxStep()
	serial, _ := dev.Serial()
	model := dev.Model()
	if model == "" {
		model = "Oasis focuser"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dev = dev
	if maxStep > 0 {
		f.maxStep = int(maxStep)
	}
	// Surface the live identity (model / firmware / hardware / serial) decoded from the
	// device in the ASCOM Description, and the firmware in DriverInfo.
	f.Desc = fmt.Sprintf("Astroasis %s — FW %s, HW %s, S/N %s (max %d steps, manual clutch)",
		model, dev.FirmwareVersion(), dev.HardwareVersion(), serial, f.maxStep)
	f.Info = fmt.Sprintf("oasisfoc Alpaca driver over pure-Go oasis-astro/oasisfoc; device FW %s build %s",
		dev.FirmwareVersion(), dev.FirmwareBuildDate())
	return true
}

func (f *OasisFocuser) openByIndex() (*oasisfoc.Oasis, error) {
	devs, err := oasisfoc.Enumerate()
	if err != nil {
		return nil, err
	}
	if f.index < 0 || f.index >= len(devs) {
		return nil, fmt.Errorf("no Oasis focuser at index %d (found %d)", f.index, len(devs))
	}
	return oasisfoc.OpenAt(devs[f.index].LocationID)
}

// --- Focuser members ---
//
// StepSize and TempComp are intentionally left at the BaseFocuser defaults (ASCOM
// PropertyNotImplemented). They are optical-train properties, not focuser-intrinsic ones:
// StepSize (microns/step) depends on the drawtube/reduction and telescope it drives, and
// temperature compensation needs the telescope's thermal-expansion coefficient — both are
// host/setup values the device cannot report. Reporting a fabricated value would be wrong;
// NotImplemented is the correct ASCOM answer.

func (f *OasisFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return false
	}
	moving, err := f.dev.Moving()
	return err == nil && moving
}

func (f *OasisFocuser) Position() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return 0, alpacadev.ErrNotConnected
	}
	p, err := f.dev.Position()
	return int(p), err
}

func (f *OasisFocuser) Temperature() (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.dev.TemperatureExternal()
}

// Move drives the absolute focuser to the given step. Returns once initiated;
// clients poll IsMoving for completion.
func (f *OasisFocuser) Move(position int) error {
	if position < 0 || (f.MaxStep() > 0 && position > f.MaxStep()) {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.MoveTo(int32(position))
}

func (f *OasisFocuser) Halt() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.StopMove()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
