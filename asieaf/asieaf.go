package main

import (
	"context"
	"fmt"
	"log"
	"strings"
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
// The EAF SDK is not safe for concurrent per-device calls and Alpaca HTTP
// handlers run concurrently, so all eaf access is serialized by mu. mu is never
// held across a sleep.
type ASIFocuser struct {
	alpacadev.BaseFocuser

	index      int
	wantSerial string // if set, bind only the focuser with this serial (hex)

	mu        sync.Mutex
	id        int  // EAF device ID (valid only while hwPresent)
	hwPresent bool // focuser physically attached and SDK handle open

	maxStep int // fixed maximum position, cached at acquire
}

// EAF is an absolute focuser, so MaxIncrement equals the full travel.
func (f *ASIFocuser) Absolute() bool    { return true }
func (f *ASIFocuser) MaxStep() int      { return f.maxStep }
func (f *ASIFocuser) MaxIncrement() int { return f.maxStep }

// NewASIFocuser creates the driver for a focuser selected by serial (preferred,
// stable) or, if serial is "", by enumeration index. The UniqueID is known up
// front from the serial, so the device is registered with a stable identity even
// before the focuser is plugged in.
func NewASIFocuser(index int, serial string) *ASIFocuser {
	f := &ASIFocuser{index: index, wantSerial: strings.ToLower(serial)}
	f.Version = "0.1.0"
	f.Info = "asieaf — ZWO EAF Alpaca driver over goasi"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser // IFocuserV4 (Platform 7)
	if f.wantSerial != "" {
		f.ID = "EAF-" + f.wantSerial
		f.DevName = "ASI EAF " + f.wantSerial
	} else {
		f.ID = fmt.Sprintf("EAF-foc%d", index) // provisional; adopts serial on first open
		f.DevName = fmt.Sprintf("ASI EAF %d", index)
	}
	return f
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca server comes up with or without a focuser attached.
func (f *ASIFocuser) Open(ctx context.Context) error {
	go f.manageHardware(ctx)
	return nil
}

// Close releases the SDK on graceful shutdown only.
func (f *ASIFocuser) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hwPresent {
		eaf.Stop(f.id)
		eaf.Close(f.id)
		f.hwPresent = false
	}
	return nil
}

// Connect is the client's presence handshake: it succeeds iff the focuser is
// attached (Connected ≡ hwPresent). It does not open hardware — the driver
// already owns it.
func (f *ASIFocuser) Connect(ctx context.Context) error {
	if !f.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports hardware presence: the device is "connected" exactly when the
// focuser is attached and its SDK handle is open.
func (f *ASIFocuser) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hwPresent
}

// Disconnect is a logical no-op: the driver owns the hardware for the life of the
// process; connection state follows the hardware, not client sessions.
func (f *ASIFocuser) Disconnect(ctx context.Context) error { return nil }

// Busy reports a transitory state in which mutating writes are rejected — here,
// while the focuser is moving. On-demand SDK read (no cached state) so an
// autofocus routine sees move-completion with no added latency.
func (f *ASIFocuser) Busy() bool { return f.IsMoving() }

// manageHardware acquires, monitors, and re-acquires the focuser for the life of
// the process. When none is present it polls; when present it pings the SDK and,
// on removal, closes the handle, drops the client session, and resumes acquiring.
func (f *ASIFocuser) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		present := f.hwPresent
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
		_, err := eaf.GetPosition(f.id)
		f.mu.Unlock()
		if err != nil {
			log.Printf("asieaf: focuser %s lost (%v); re-acquiring", f.ID, err)
			f.mu.Lock()
			eaf.Close(f.id)
			f.hwPresent = false // Connected() follows this; gate returns NotConnected
			f.mu.Unlock()
			continue
		}
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire scans connected focusers for the target (by serial, else the
// configured index), opens it, and caches its properties. Returns true once the
// focuser is open and ready.
func (f *ASIFocuser) tryAcquire() bool {
	n := eaf.GetNum()
	for i := 0; i < n; i++ {
		id, err := eaf.GetID(i)
		if err != nil {
			continue
		}
		if f.wantSerial == "" && i != f.index {
			continue
		}
		if err := eaf.Open(id); err != nil {
			continue
		}
		sn, _ := eaf.GetSerialNumber(id)
		if f.wantSerial != "" && !strings.EqualFold(sn, f.wantSerial) {
			eaf.Close(id)
			continue
		}
		f.configureOpened(id, sn)
		return true
	}
	return false
}

// configureOpened caches a freshly opened focuser's properties and publishes it
// as the live handle.
func (f *ASIFocuser) configureOpened(id int, serialHex string) {
	info, _ := eaf.GetProperty(id)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.id = id
	f.maxStep = info.MaxStep
	if info.Name != "" {
		f.DevName = info.Name
	}
	f.Desc = fmt.Sprintf("ZWO %s (max %d steps)", info.Name, info.MaxStep)
	if f.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		f.ID = "EAF-" + serialHex // adopt the real serial when not pinned by flag
	}
	f.hwPresent = true
}

// --- Focuser members ---

func (f *ASIFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hwPresent {
		return false
	}
	moving, _, err := eaf.IsMoving(f.id)
	return err == nil && moving
}

func (f *ASIFocuser) Position() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hwPresent {
		return 0, alpacadev.ErrNotConnected
	}
	return eaf.GetPosition(f.id)
}

func (f *ASIFocuser) Temperature() (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hwPresent {
		return 0, alpacadev.ErrNotConnected
	}
	t, err := eaf.GetTemp(f.id)
	return float64(t), err
}

// Move drives the absolute focuser to the given step position. It returns once
// the move is initiated; clients poll IsMoving for completion.
func (f *ASIFocuser) Move(position int) error {
	if position < 0 || position > f.maxStep {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hwPresent {
		return alpacadev.ErrNotConnected
	}
	return eaf.Move(f.id, position)
}

func (f *ASIFocuser) Halt() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hwPresent {
		return alpacadev.ErrNotConnected
	}
	return eaf.Stop(f.id)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
