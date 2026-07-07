// Package driver is the ASCOM Alpaca FilterWheel device for the Astroasis Oasis
// filter wheel, over the Go oasis-astro/oasisfw library (USB-HID). It is
// served standalone by cmd/oasisfw and hosted by the astrofleet aggregator.
package driver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/oasis-astro/oasisfw"
)

var _ alpacadev.FilterWheel = (*OasisWheel)(nil)

// OasisWheel adapts an oasis-astro/oasisfw filter wheel to the alpacadev.FilterWheel
// + Hardware interfaces. The oasisfw library already presents 0-based positions and
// -1 while moving (ASCOM convention), so the adapter is a thin pass-through.
type OasisWheel struct {
	alpacadev.BaseFilterWheel

	index int

	mu  sync.Mutex
	dev *oasisfw.Oasis // open handle; nil when no wheel is attached

	slots   int
	names   []string
	offsets []int

	openDev func() (*oasisfw.Oasis, error)
}

// NewOasisWheel creates the driver for the wheel at the given enumeration index.
func NewOasisWheel(index int) *OasisWheel {
	w := &OasisWheel{index: index}
	w.Version = "0.1.0"
	w.Info = "oasisfw — Astroasis Oasis filter wheel Alpaca driver over Go oasis-astro/oasisfw"
	w.IfaceVer = alpacadev.InterfaceVersionFilterWheel
	w.ID = fmt.Sprintf("Oasis-fw%d", index)
	w.DevName = fmt.Sprintf("Oasis Filter Wheel %d", index)
	w.openDev = w.openByIndex
	return w
}

// --- Hardware lifecycle ---

func (w *OasisWheel) Open(ctx context.Context) error {
	if w.openDev == nil {
		w.openDev = w.openByIndex
	}
	go alpacadev.Supervise(ctx, w.ID, func() { w.manageHardware(ctx) })
	return nil
}

func (w *OasisWheel) Close(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dev != nil {
		w.dev.Close()
		w.dev = nil
	}
	return nil
}

func (w *OasisWheel) Connect(ctx context.Context) error {
	if !w.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (w *OasisWheel) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dev != nil
}

func (w *OasisWheel) Disconnect(ctx context.Context) error { return nil }

// Busy rejects mutating writes while the wheel is turning (Position is -1 then).
func (w *OasisWheel) Busy() bool { return w.Position() < 0 }

func (w *OasisWheel) manageHardware(ctx context.Context) {
	// Release the device handle when the supervised loop ends (ctx cancelled), else the
	// HID handle leaks; on macOS/IOKit that can wedge the device until a replug.
	defer func() {
		w.mu.Lock()
		if w.dev != nil {
			w.dev.Close()
			w.dev = nil
		}
		w.mu.Unlock()
	}()
	for ctx.Err() == nil {
		w.mu.Lock()
		present := w.dev != nil
		w.mu.Unlock()
		if !present {
			if w.tryAcquire() {
				log.Printf("oasisfw: filter wheel %s acquired", w.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		w.mu.Lock()
		var err error
		if w.dev != nil {
			_, err = w.dev.State()
		}
		if err != nil {
			log.Printf("oasisfw: filter wheel %s lost (%v); re-acquiring", w.ID, err)
			w.dev.Close()
			w.dev = nil
		}
		w.mu.Unlock()
		sleepCtx(ctx, 2*time.Second)
	}
}

func (w *OasisWheel) tryAcquire() bool {
	dev, err := w.openDev()
	if err != nil {
		return false
	}
	w.configureOpened(dev)
	return true
}

func (w *OasisWheel) openByIndex() (*oasisfw.Oasis, error) {
	devs, err := oasisfw.Enumerate()
	if err != nil {
		return nil, err
	}
	if w.index < 0 || w.index >= len(devs) {
		return nil, fmt.Errorf("no Oasis filter wheel at index %d (found %d)", w.index, len(devs))
	}
	return oasisfw.OpenAt(devs[w.index].LocationID)
}

// configureOpened caches the wheel's slot layout (count, names, focus offsets),
// padding names/offsets to the slot count for a consistent ASCOM presentation.
func (w *OasisWheel) configureOpened(dev *oasisfw.Oasis) {
	slots, _ := dev.Slots()
	names, _ := dev.Names()
	off32, _ := dev.FocusOffsets()
	serial, _ := dev.Serial()
	model := dev.Model()
	if model == "" {
		model = "Oasis filter wheel"
	}

	full := make([]string, slots)
	for i := range full {
		if i < len(names) && names[i] != "" {
			full[i] = names[i]
		} else {
			full[i] = fmt.Sprintf("Filter %d", i+1)
		}
	}
	offsets := make([]int, slots)
	for i := 0; i < slots && i < len(off32); i++ {
		offsets[i] = int(off32[i])
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.dev = dev
	w.slots = slots
	w.names = full
	w.offsets = offsets
	w.Desc = fmt.Sprintf("Astroasis %s — FW %s, HW %s, S/N %s (%d slots)",
		model, dev.FirmwareVersion(), dev.HardwareVersion(), serial, slots)
	w.Info = fmt.Sprintf("oasisfw Alpaca driver over Go oasis-astro/oasisfw; device FW %s build %s",
		dev.FirmwareVersion(), dev.FirmwareBuildDate())
}

// --- FilterWheel members ---

// Position returns the current 0-based slot, or -1 while moving or disconnected.
func (w *OasisWheel) Position() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dev == nil {
		return -1
	}
	pos, err := w.dev.Position()
	if err != nil {
		return -1
	}
	return pos // oasisfw already returns 0-based, -1 while busy
}

// SetPosition initiates a move to the given 0-based slot. Returns once initiated;
// clients poll Position (-1 while moving) for completion.
func (w *OasisWheel) SetPosition(slot int) error {
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

func (w *OasisWheel) Names() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

func (w *OasisWheel) FocusOffsets() []int {
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
