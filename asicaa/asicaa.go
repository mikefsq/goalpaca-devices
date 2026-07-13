// Package driver is the ASCOM Alpaca Rotator device for the ZWO CAA (Camera Angle
// Adjuster), over goasi/caa (cgo, the ZWO CAA SDK). It is served standalone by
// cmd/asicaa; being cgo + SDK, it is not built into the vendor-free alpacahurd.
package driver

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/caa"
)

// Compile-time check that the driver satisfies the Alpaca Rotator interface.
var _ alpacadev.Rotator = (*ASIRotator)(nil)

// ASIRotator adapts a goasi/caa rotator (ZWO Camera Angle Adjuster) to the
// alpacadev.Rotator + Hardware interfaces.
//
// Position model: the hardware angle from caa.GetDegree is the ASCOM
// MechanicalPosition. Sky Position is mechanical + syncOffset (set by Sync).
// Reverse is delegated to the hardware.
//
// The CAA SDK is not safe for concurrent per-device calls; all caa access is
// serialized by mu. mu is never held across a sleep.
type ASIRotator struct {
	alpacadev.BaseRotator

	index      int
	wantSerial string // if set, bind only the rotator with this serial (hex)

	mu        sync.Mutex
	id        int  // CAA device ID (valid only while hwPresent)
	hwPresent bool // rotator physically attached and SDK handle open

	syncOffset float64 // sky = mechanical + syncOffset (degrees)
	target     float64 // last commanded sky target (degrees)
	haveTarget bool
}

// CanReverse reports that the hardware supports reversing direction.
func (r *ASIRotator) CanReverse() bool { return true }

// NewASIRotator creates the driver for a rotator selected by serial, or by
// enumeration index if serial is "". With a serial the device has a stable
// identity before the rotator is plugged in.
func NewASIRotator(index int, serial string) *ASIRotator {
	r := &ASIRotator{index: index, wantSerial: strings.ToLower(serial)}
	r.Version = "0.1.0"
	r.Info = "asicaa — ZWO CAA Alpaca rotator driver over goasi"
	r.IfaceVer = alpacadev.InterfaceVersionRotator // IRotatorV4 (Platform 7)
	if r.wantSerial != "" {
		r.ID = "CAA-" + r.wantSerial
		r.DevName = "ASI CAA " + r.wantSerial
	} else {
		r.ID = fmt.Sprintf("CAA-rot%d", index) // provisional; adopts serial on first open
		r.DevName = fmt.Sprintf("ASI CAA %d", index)
	}
	return r
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca server comes up with or without a rotator attached.
func (r *ASIRotator) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, r.ID, func() { r.manageHardware(ctx) })
	return nil
}

// Close releases the SDK on graceful shutdown only.
func (r *ASIRotator) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hwPresent {
		caa.Stop(r.id)
		caa.Close(r.id)
		r.hwPresent = false
	}
	return nil
}

// Connect succeeds iff the rotator is attached. It does not open hardware.
func (r *ASIRotator) Connect(ctx context.Context) error {
	if !r.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports true when the rotator is attached and its SDK handle is open.
func (r *ASIRotator) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hwPresent
}

// Disconnect is a no-op: connection state follows the hardware, not client sessions.
func (r *ASIRotator) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes while the rotator is moving.
func (r *ASIRotator) Busy() bool { return r.IsMoving() }

// manageHardware acquires, monitors, and re-acquires the rotator for the life of
// the process: polls when absent, pings the SDK when present, closes the handle
// on removal.
func (r *ASIRotator) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		r.mu.Lock()
		present := r.hwPresent
		r.mu.Unlock()

		if !present {
			if r.tryAcquire() {
				log.Printf("asicaa: rotator %s acquired", r.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}

		// Monitor: a healthy rotator reports its angle without error.
		r.mu.Lock()
		_, err := caa.GetDegree(r.id)
		r.mu.Unlock()
		if err != nil {
			log.Printf("asicaa: rotator %s lost (%v); re-acquiring", r.ID, err)
			r.mu.Lock()
			caa.Close(r.id)
			r.hwPresent = false
			r.mu.Unlock()
			continue
		}
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire scans connected rotators for the target (by serial, else the
// configured index) and opens it. Returns true once the rotator is open and ready.
func (r *ASIRotator) tryAcquire() bool {
	n := caa.GetNum()
	for i := 0; i < n; i++ {
		id, err := caa.GetID(i)
		if err != nil {
			continue
		}
		if r.wantSerial == "" && i != r.index {
			continue
		}
		if err := caa.Open(id); err != nil {
			continue
		}
		sn, _ := caa.GetSerialNumber(id)
		if r.wantSerial != "" && !strings.EqualFold(sn, r.wantSerial) {
			caa.Close(id)
			continue
		}
		r.configureOpened(id, sn)
		return true
	}
	return false
}

// configureOpened caches a freshly opened rotator's properties and publishes it
// as the live handle.
func (r *ASIRotator) configureOpened(id int, serialHex string) {
	info, _ := caa.GetProperty(id)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.id = id
	if info.Name != "" {
		r.DevName = info.Name
	}
	r.Desc = fmt.Sprintf("ZWO %s rotator", info.Name)
	if r.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		r.ID = "CAA-" + serialHex // adopt real serial when not pinned by flag
	}
	r.hwPresent = true
}

// --- Rotator members ---

// mechanicalLocked reads the raw hardware angle. Caller holds mu.
func (r *ASIRotator) mechanicalLocked() (float64, bool) {
	if !r.hwPresent {
		return 0, false
	}
	deg, err := caa.GetDegree(r.id)
	if err != nil {
		return 0, false
	}
	return norm360(float64(deg)), true
}

func (r *ASIRotator) MechanicalPosition() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, _ := r.mechanicalLocked()
	return m
}

func (r *ASIRotator) Position() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, _ := r.mechanicalLocked()
	return norm360(m + r.syncOffset)
}

func (r *ASIRotator) TargetPosition() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.haveTarget {
		return r.target
	}
	m, _ := r.mechanicalLocked()
	return norm360(m + r.syncOffset)
}

func (r *ASIRotator) IsMoving() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return false
	}
	moving, _, err := caa.IsMoving(r.id)
	return err == nil && moving
}

func (r *ASIRotator) Reverse() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return false
	}
	rev, _ := caa.GetReverse(r.id)
	return rev
}

func (r *ASIRotator) SetReverse(reversed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return alpacadev.ErrNotConnected
	}
	return caa.SetReverse(r.id, reversed)
}

// MoveAbsolute slews to an absolute sky angle (degrees). Initiator; poll IsMoving.
func (r *ASIRotator) MoveAbsolute(position float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return alpacadev.ErrNotConnected
	}
	sky := norm360(position)
	r.target, r.haveTarget = sky, true
	return caa.MoveTo(r.id, float32(norm360(sky-r.syncOffset)))
}

// MoveMechanical slews to an absolute mechanical angle (degrees).
func (r *ASIRotator) MoveMechanical(position float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return alpacadev.ErrNotConnected
	}
	mech := norm360(position)
	r.target, r.haveTarget = norm360(mech+r.syncOffset), true
	return caa.MoveTo(r.id, float32(mech))
}

// Move slews by a relative offset (degrees) from the current sky position.
func (r *ASIRotator) Move(relative float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return alpacadev.ErrNotConnected
	}
	m, ok := r.mechanicalLocked()
	if ok {
		r.target, r.haveTarget = norm360(m+r.syncOffset+relative), true
	}
	return caa.Move(r.id, float32(relative))
}

// Sync redefines the current sky position to the given angle by adjusting the
// software offset, without moving.
func (r *ASIRotator) Sync(position float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.mechanicalLocked()
	if !ok {
		return alpacadev.ErrNotConnected
	}
	r.syncOffset = norm360(norm360(position) - m)
	return nil
}

func (r *ASIRotator) Halt() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hwPresent {
		return alpacadev.ErrNotConnected
	}
	return caa.Stop(r.id)
}

// norm360 normalizes an angle into [0, 360).
func norm360(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
