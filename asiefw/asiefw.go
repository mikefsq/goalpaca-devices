// Package driver is the ASCOM Alpaca FilterWheel device for the ZWO EFW, over the
// Go goasi/efw driver (USB-HID, no ZWO SDK). It is served standalone by
// cmd/asiefw and hosted by the alpacahurd aggregator.
package driver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/efw"
)

// Compile-time check that the driver satisfies the Alpaca FilterWheel interface.
var _ alpacadev.FilterWheel = (*ASIFilterWheel)(nil)

// ASIFilterWheel adapts a goasi/efw filter wheel to the alpacadev.FilterWheel +
// Hardware interfaces.
//
// The EFW SDK is not safe for concurrent per-device calls; all efw access is
// serialized by mu. mu is never held across a sleep.
type ASIFilterWheel struct {
	alpacadev.BaseFilterWheel

	index          int
	wantSerial     string // if set, bind only the wheel with this serial (hex)
	unidirectional bool   // move mode, re-applied on every (re)acquire

	mu  sync.Mutex
	dev *efw.EFW // open handle; nil when no wheel is attached

	slots   int      // number of filter slots, cached at acquire
	names   []string // per-slot names (defaults; client-presented)
	offsets []int    // per-slot focus offsets (default zero)

	// openDev opens the target wheel; set once at construction. Defaults to
	// openConfigured (serial/index). Tests override it to inject a
	// fake-transport-backed device and exercise the full stack without hardware.
	openDev func() (*efw.EFW, error)
}

// NewASIFilterWheel creates the driver for a wheel selected by serial, or by
// enumeration index if serial is "". With a serial the device has a stable
// identity before the wheel is plugged in.
func NewASIFilterWheel(index int, serial string, unidirectional bool) *ASIFilterWheel {
	w := &ASIFilterWheel{index: index, wantSerial: strings.ToLower(serial), unidirectional: unidirectional}
	w.Version = "0.1.0"
	w.Info = "asiefw — ZWO EFW Alpaca driver over goasi"
	w.IfaceVer = alpacadev.InterfaceVersionFilterWheel // IFilterWheelV3 (Platform 7)
	if w.wantSerial != "" {
		w.ID = "EFW-" + w.wantSerial
		w.DevName = "ASI EFW " + w.wantSerial
	} else {
		w.ID = fmt.Sprintf("EFW-fw%d", index) // provisional; adopts serial on first open
		w.DevName = fmt.Sprintf("ASI EFW %d", index)
	}
	w.openDev = w.openConfigured
	return w
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca server comes up with or without a wheel attached.
func (w *ASIFilterWheel) Open(ctx context.Context) error {
	go alpacadev.Supervise(ctx, w.ID, func() { w.manageHardware(ctx) })
	return nil
}

// Close releases the device on graceful shutdown only.
func (w *ASIFilterWheel) Close(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dev != nil {
		w.dev.Close()
		w.dev = nil
	}
	return nil
}

// Connect succeeds iff the wheel is attached. It does not open hardware.
func (w *ASIFilterWheel) Connect(ctx context.Context) error {
	if !w.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports true when the wheel is attached and its handle is open.
func (w *ASIFilterWheel) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dev != nil
}

// Disconnect is a no-op: connection state follows the hardware, not client sessions.
func (w *ASIFilterWheel) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes while the wheel is moving. The EFW SDK reports
// position -1 while turning.
func (w *ASIFilterWheel) Busy() bool { return w.Position() < 0 }

// manageHardware acquires, monitors, and re-acquires the wheel for the life of
// the process: polls when absent, pings the SDK when present, closes the handle
// on removal.
func (w *ASIFilterWheel) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.Lock()
		present := w.dev != nil
		w.mu.Unlock()

		if !present {
			if w.tryAcquire() {
				log.Printf("asiefw: filter wheel %s acquired", w.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}

		// Monitor: a healthy wheel reports its position without error.
		w.mu.Lock()
		_, err := w.dev.Position()
		w.mu.Unlock()
		if err != nil {
			log.Printf("asiefw: filter wheel %s lost (%v); re-acquiring", w.ID, err)
			w.mu.Lock()
			w.dev.Close()
			w.dev = nil
			w.mu.Unlock()
			continue
		}
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire opens the target wheel (by serial, else by index) and caches its
// slot layout. Returns true once the wheel is open and ready.
func (w *ASIFilterWheel) tryAcquire() bool {
	dev, err := w.openDev()
	if err != nil {
		return false
	}
	w.configureOpened(dev)
	return true
}

// openConfigured opens the target wheel by serial, else by enumeration index. It
// is the default openDev.
func (w *ASIFilterWheel) openConfigured() (*efw.EFW, error) {
	if w.wantSerial != "" {
		return efw.OpenBySerial(w.wantSerial)
	}
	return w.openByIndex()
}

// openByIndex opens the wheel at the configured enumeration index. Index order is
// not stable across replug.
func (w *ASIFilterWheel) openByIndex() (*efw.EFW, error) {
	devs, err := efw.Enumerate()
	if err != nil {
		return nil, err
	}
	if w.index < 0 || w.index >= len(devs) {
		return nil, fmt.Errorf("no EFW at index %d (%d attached)", w.index, len(devs))
	}
	return efw.OpenAt(devs[w.index].LocationID)
}

// configureOpened caches a freshly opened wheel's slot layout and publishes it as
// the live handle. The slot count can be 0 right after connection while the wheel
// detects it, so it is polled until known (bounded).
func (w *ASIFilterWheel) configureOpened(dev *efw.EFW) {
	dev.SetUnidirectional(w.unidirectional) // host-side; re-applied each acquire
	slots := 0
	for tries := 0; tries < 50; tries++ {
		if s := dev.Slots(); s > 0 {
			slots = s
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	names := make([]string, slots)
	offsets := make([]int, slots)
	for i := range names {
		names[i] = fmt.Sprintf("Filter %d", i+1)
	}
	model, _ := dev.Model()
	serial, _ := dev.SerialZWO()

	w.mu.Lock()
	defer w.mu.Unlock()
	w.dev = dev
	w.slots = slots
	w.names = names
	w.offsets = offsets
	if model != "" {
		w.DevName = model
	}
	w.Desc = fmt.Sprintf("ZWO %s (%d slots)", model, slots)
	if w.wantSerial == "" && serial != "" {
		w.ID = "EFW-" + serial // adopt real serial when not pinned by flag
	}
}

// --- FilterWheel members ---

// Position returns the current slot (0-based), or -1 while the wheel is moving or
// when no wheel is connected.
func (w *ASIFilterWheel) Position() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dev == nil {
		return -1
	}
	pos, err := w.dev.Position()
	if err != nil {
		return -1
	}
	return pos // already reports -1 while moving
}

// SetPosition initiates a move to the given slot (0-based). It returns once the
// move is initiated; clients poll Position (-1 while moving) for completion.
func (w *ASIFilterWheel) SetPosition(slot int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dev == nil {
		return alpacadev.ErrNotConnected
	}
	if slot < 0 || slot >= w.slots {
		return alpacadev.ErrInvalidValue
	}
	return w.dev.SetPosition(slot)
}

func (w *ASIFilterWheel) Names() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

func (w *ASIFilterWheel) FocusOffsets() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.offsets...)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
