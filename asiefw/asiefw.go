package main

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
// Hardware interfaces. It is the device-specific code: it knows about goasi/ZWO;
// the library does not.
//
// The EFW SDK is not safe for concurrent per-device calls and Alpaca HTTP
// handlers run concurrently, so all efw access is serialized by mu. mu is never
// held across a sleep.
type ASIFilterWheel struct {
	alpacadev.BaseFilterWheel

	index      int
	wantSerial string // if set, bind only the wheel with this serial (hex)

	mu        sync.Mutex
	id        int  // EFW device ID (valid only while hwPresent)
	hwPresent bool // wheel physically attached and SDK handle open

	slots   int      // number of filter slots, cached at acquire
	names   []string // per-slot names (defaults; client-presented)
	offsets []int    // per-slot focus offsets (default zero)
}

// NewASIFilterWheel creates the driver for a wheel selected by serial (preferred,
// stable) or, if serial is "", by enumeration index. The UniqueID is known up
// front from the serial, so the device is registered with a stable identity even
// before the wheel is plugged in.
func NewASIFilterWheel(index int, serial string) *ASIFilterWheel {
	w := &ASIFilterWheel{index: index, wantSerial: strings.ToLower(serial)}
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
	return w
}

// --- Hardware lifecycle (persistent owner) ---

// Open starts the hardware-management goroutine and returns immediately, so the
// Alpaca server comes up with or without a wheel attached.
func (w *ASIFilterWheel) Open(ctx context.Context) error {
	go w.manageHardware(ctx)
	return nil
}

// Close releases the SDK on graceful shutdown only.
func (w *ASIFilterWheel) Close(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.hwPresent {
		efw.Close(w.id)
		w.hwPresent = false
	}
	return nil
}

// Connect is the client's presence handshake: it succeeds iff the wheel is
// attached (Connected ≡ hwPresent). It does not open hardware — the driver
// already owns it.
func (w *ASIFilterWheel) Connect(ctx context.Context) error {
	if !w.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

// Connected reports hardware presence: the device is "connected" exactly when the
// wheel is attached and its SDK handle is open.
func (w *ASIFilterWheel) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwPresent
}

// Disconnect is a logical no-op: the driver owns the hardware for the life of the
// process; connection state follows the hardware, not client sessions.
func (w *ASIFilterWheel) Disconnect(ctx context.Context) error { return nil }

// Busy reports a transitory state in which mutating writes are rejected — here,
// while the wheel is moving. The EFW SDK reports position -1 while turning.
// On-demand read (only reached when Connected, so -1 means moving).
func (w *ASIFilterWheel) Busy() bool { return w.Position() < 0 }

// manageHardware acquires, monitors, and re-acquires the wheel for the life of
// the process. When none is present it polls; when present it pings the SDK and,
// on removal, closes the handle, drops the client session, and resumes acquiring.
func (w *ASIFilterWheel) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.Lock()
		present := w.hwPresent
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
		_, err := efw.GetPosition(w.id)
		w.mu.Unlock()
		if err != nil {
			log.Printf("asiefw: filter wheel %s lost (%v); re-acquiring", w.ID, err)
			w.mu.Lock()
			efw.Close(w.id)
			w.hwPresent = false // Connected() follows this; gate returns NotConnected
			w.mu.Unlock()
			continue
		}
		sleepCtx(ctx, 2*time.Second)
	}
}

// tryAcquire scans connected wheels for the target (by serial, else the
// configured index), opens it, and caches its slot layout. Returns true once the
// wheel is open and ready.
func (w *ASIFilterWheel) tryAcquire() bool {
	n := efw.GetNum()
	for i := 0; i < n; i++ {
		id, err := efw.GetID(i)
		if err != nil {
			continue
		}
		if w.wantSerial == "" && i != w.index {
			continue
		}
		if err := efw.Open(id); err != nil {
			continue
		}
		sn, _ := efw.GetSerialNumber(id)
		if w.wantSerial != "" && !strings.EqualFold(sn, w.wantSerial) {
			efw.Close(id)
			continue
		}
		w.configureOpened(id, sn)
		return true
	}
	return false
}

// configureOpened caches a freshly opened wheel's slot layout and publishes it as
// the live handle. SlotNum can be 0 immediately after connection while the wheel
// detects its slot count, so it is read until known (bounded).
func (w *ASIFilterWheel) configureOpened(id int, serialHex string) {
	var info efw.Info
	for tries := 0; tries < 50; tries++ {
		info, _ = efw.GetProperty(id)
		if info.SlotNum > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	names := make([]string, info.SlotNum)
	offsets := make([]int, info.SlotNum)
	for i := range names {
		names[i] = fmt.Sprintf("Filter %d", i+1)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.id = id
	w.slots = info.SlotNum
	w.names = names
	w.offsets = offsets
	if info.Name != "" {
		w.DevName = info.Name
	}
	w.Desc = fmt.Sprintf("ZWO %s (%d slots)", info.Name, info.SlotNum)
	if w.wantSerial == "" && strings.Trim(serialHex, "0") != "" {
		w.ID = "EFW-" + serialHex // adopt the real serial when not pinned by flag
	}
	w.hwPresent = true
}

// --- FilterWheel members ---

// Position returns the current slot (0-based), or -1 while the wheel is moving or
// when no wheel is connected.
func (w *ASIFilterWheel) Position() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hwPresent {
		return -1
	}
	pos, err := efw.GetPosition(w.id)
	if err != nil {
		return -1
	}
	return pos // SDK already reports -1 while moving
}

// SetPosition initiates a move to the given slot (0-based). It returns once the
// move is initiated; clients poll Position (-1 while moving) for completion.
func (w *ASIFilterWheel) SetPosition(slot int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hwPresent {
		return alpacadev.ErrNotConnected
	}
	if slot < 0 || slot >= w.slots {
		return alpacadev.ErrInvalidValue
	}
	return efw.SetPosition(w.id, slot)
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
