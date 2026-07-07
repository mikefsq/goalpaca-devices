// Package driver is the ASCOM Alpaca Focuser device for the Astroasis Oasis
// focuser, over the Go oasis-astro/oasisfoc library (USB-HID). It is served
// standalone by cmd/oasisfoc and hosted by the astrofleet aggregator.
package driver

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfoc"
)

var _ alpacadev.Focuser = (*OasisFocuser)(nil)

// debugMoves enables verbose move/settle tracing on the console. Off by default; set
// OASISFOC_DEBUG=1 (or true/yes/on) to enable.
var debugMoves = func() bool {
	switch strings.ToLower(os.Getenv("OASISFOC_DEBUG")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}()

// OasisFocuser adapts an oasis-astro/oasisfoc focuser to the alpacadev.Focuser +
// Hardware interfaces. The focuser has an absolute encoder (MoveTo/Position), so it
// presents as an ASCOM absolute focuser; it also has a manual clutch, so the reported
// position can change without a commanded move (the encoder still tracks it).
type OasisFocuser struct {
	alpacadev.BaseFocuser

	index int

	mu  sync.Mutex
	dev *oasisfoc.Oasis // open handle; nil when no focuser is attached

	maxStep int // device-reported travel, cached at acquire

	// move/settle tracing (guarded by mu; populated only when debugMoves).
	moveStart  time.Time
	moveTarget int
	expectMove bool
	sawMoving  bool
	prevMoving bool

	// openDev opens the target focuser; set once at construction.
	openDev func() (*oasisfoc.Oasis, error)
}

// NewOasisFocuser creates the driver for the focuser at the given enumeration index.
func NewOasisFocuser(index int) *OasisFocuser {
	f := &OasisFocuser{index: index}
	f.Version = "0.1.0"
	f.Info = "oasisfoc — Astroasis Oasis focuser Alpaca driver over Go oasis-astro/oasisfoc"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = fmt.Sprintf("Oasis-foc%d", index)
	f.DevName = fmt.Sprintf("Oasis Focuser %d", index)
	f.openDev = f.openByIndex
	return f
}

// Absolute reports the focuser as absolute (MaxIncrement equals full travel).
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
	// Release the device handle when the supervised loop ends (ctx cancelled), else the
	// HID handle leaks; on macOS/IOKit that can wedge the device until a replug.
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
	// Surface the device identity (model/firmware/hardware/serial) in ASCOM Description,
	// firmware in DriverInfo.
	f.Desc = fmt.Sprintf("Astroasis %s — FW %s, HW %s, S/N %s (max %d steps, manual clutch)",
		model, dev.FirmwareVersion(), dev.HardwareVersion(), serial, f.maxStep)
	f.Info = fmt.Sprintf("oasisfoc Alpaca driver over Go oasis-astro/oasisfoc; device FW %s build %s",
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
// StepSize and TempComp are left at the BaseFocuser defaults (ASCOM
// PropertyNotImplemented): they are optical-train values the device cannot report.

func (f *OasisFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return false
	}
	moving, err := f.dev.Moving()
	if err != nil {
		return false
	}
	f.traceMovingLocked(moving)
	return moving
}

// traceMovingLocked logs the device "moving" flag and position per poll while a
// commanded move is in flight (also catches manual-clutch moves). Quiet between moves.
// Caller holds mu.
func (f *OasisFocuser) traceMovingLocked(moving bool) {
	if !debugMoves {
		return
	}
	if !moving && !f.expectMove && !f.prevMoving {
		return // idle
	}
	pos, _ := f.dev.Position()
	elapsed := ""
	if !f.moveStart.IsZero() {
		elapsed = fmt.Sprintf(" t=%.1fs", time.Since(f.moveStart).Seconds())
	}
	switch {
	case moving:
		f.sawMoving = true
		log.Printf("oasisfoc: %s ismoving=true  pos=%d target=%d%s", f.ID, pos, f.moveTarget, elapsed)
	case f.expectMove && !f.sawMoving && !f.moveStart.IsZero() && time.Since(f.moveStart) < 2*time.Second:
		// commanded move not started yet (firmware arming)
		log.Printf("oasisfoc: %s ismoving=false pos=%d target=%d%s (awaiting motion)", f.ID, pos, f.moveTarget, elapsed)
	default:
		log.Printf("oasisfoc: %s ismoving=false pos=%d target=%d%s  <-- DONE (device idle)", f.ID, pos, f.moveTarget, elapsed)
		f.expectMove = false
		f.sawMoving = false
	}
	f.prevMoving = moving
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

// Synchronous-completion tuning for SHORT moves. A move of at most syncMoveMaxSteps
// completes synchronously inside Move so a client that checks IsMoving immediately after
// sees completion on the first read. Larger moves stay async (blocking would risk the
// client's HTTP timeout and defeat Halt/progress polling).
const (
	syncMoveMaxSteps = 250                   // moves this short (in steps) complete synchronously
	syncMoveCap      = 1 * time.Second       // hard ceiling on the synchronous block (safety)
	syncMovePoll     = 20 * time.Millisecond // moving-flag re-check interval while blocking
	syncMoveTol      = 2                     // position-at-target tolerance (steps)
)

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Move drives the absolute focuser to the given step. A long move returns once initiated
// (async — clients poll IsMoving). A short move blocks until the device reports idle AND
// at target (bounded by syncMoveCap) so a fast client poll resolves on the first read.
func (f *OasisFocuser) Move(position int) error {
	if position < 0 || (f.MaxStep() > 0 && position > f.MaxStep()) {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	if f.dev == nil {
		f.mu.Unlock()
		return alpacadev.ErrNotConnected
	}
	cur, curErr := f.dev.Position()
	// Duplicate / zero-distance move: already at target. Don't re-issue MoveTo (could
	// trigger a needless backlash-comp move) and skip the settle block; return immediately.
	if curErr == nil && position == int(cur) {
		f.mu.Unlock()
		if debugMoves {
			log.Printf("oasisfoc: %s MOVE target=%d already at position, no-op", f.ID, position)
		}
		return nil
	}
	if debugMoves {
		f.moveStart = time.Now()
		f.moveTarget = position
		f.expectMove = true
		f.sawMoving = false
		f.prevMoving = false
		log.Printf("oasisfoc: %s MOVE target=%d from=%d", f.ID, position, cur)
	}
	err := f.dev.MoveTo(int32(position))
	f.mu.Unlock()
	if err != nil {
		return err
	}

	// Only short moves finish synchronously. If the current position was unreadable, stay
	// async (can't bound the distance).
	if curErr != nil || absInt(position-int(cur)) > syncMoveMaxSteps {
		return nil
	}
	// Block until motion ends: device idle AND either at target or already seen moving
	// (so it has since stopped — target reached, concurrent /halt, or soft-limit stop).
	// observedMoving guards the firmware-arming window, where the device asserts "moving"
	// a few ms after MoveTo. A concurrent /halt takes f.mu between polls to issue StopMove.
	start := time.Now()
	observedMoving := false
	for time.Since(start) < syncMoveCap {
		time.Sleep(syncMovePoll)
		f.mu.Lock()
		moving, pos := false, -1
		if f.dev != nil {
			if m, e := f.dev.Moving(); e == nil {
				moving = m
			}
			if p, e := f.dev.Position(); e == nil {
				pos = int(p)
			}
		}
		if moving {
			observedMoving = true
		}
		atTarget := pos >= 0 && absInt(pos-position) <= syncMoveTol
		done := !moving && (atTarget || observedMoving)
		if done && debugMoves && f.expectMove {
			note := "DONE (synchronous)"
			if !atTarget {
				note = "stopped short (halt/limit)"
			}
			log.Printf("oasisfoc: %s ismoving=false pos=%d target=%d t=%.1fs  <-- %s",
				f.ID, pos, position, time.Since(f.moveStart).Seconds(), note)
			f.expectMove = false
		}
		f.mu.Unlock()
		if done {
			break
		}
	}
	return nil
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
