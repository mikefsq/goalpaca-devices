// Package driver is the ASCOM Alpaca front-end for the ASIAIR power-distribution
// board, over the pure-Go asiair library. The board presents one ASCOM device
// type — a Switch carrying the four power ports (two of them dimmable), the DSLR
// shutter, the auto-dew enables, and the ten telemetry readings — with the
// weather push, the auto-dew ramp and the timed DSLR frame sequence on the Action
// seam. Served standalone by cmd/asiair and hosted by the alpacahurd aggregator.
package driver

import (
	"context"
	"log"
	"sync"

	"github.com/mikefsq/asiair"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Hub owns the single asiair.Board the device drives. It exists for the same
// reason smpro's does — one board handle, refcounted across the server's
// per-device Hardware lifecycle — but here it carries a second responsibility
// that is not optional.
//
// A GPIO character-device line request is owned by its fd. Close the fd and the
// kernel reverts the line to an input, where the board's pull-down switches the
// port OFF. Ports 3 and 4 are where the camera and the mount live. So the Board
// must stay open for the life of the process: Close is the server's Hardware
// teardown, and Disconnect is deliberately a no-op (see Switch.Disconnect). An
// ASCOM client reconnecting mid-session must not power-cycle the mount.
//
// The asymmetry is what makes it dangerous rather than merely wrong: the dimmable
// ports run through sysfs PWM, which SURVIVES a process exit. A driver that
// closed its lines would cut the mount and leave the dew heater running.
type Hub struct {
	mu    sync.Mutex
	board *asiair.Board
	refs  int

	cfg asiair.Config

	// openBoard opens the hardware; a test replaces it to inject a Board over the
	// asiair bus fakes.
	openBoard func() (*asiair.Board, []error, error)
}

// NewHub creates the board owner for the given asiair config.
func NewHub(cfg asiair.Config) *Hub {
	return &Hub{
		cfg: cfg,
		openBoard: func() (*asiair.Board, []error, error) {
			return asiair.Open(cfg)
		},
	}
}

// Config returns the board configuration. The Switch builds its slot array from
// this: which ports are dimmable is a property of the Pi's pin functions and the
// applied overlay, so it is fixed for the session and can back the static ASCOM
// metadata (MinSwitchValue / MaxSwitchValue / SwitchStep) that ISwitchV3 requires.
func (h *Hub) Config() asiair.Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

// Open opens the board on the first call and refcounts the rest.
//
// Warnings are logged rather than fatal, matching the library's best-effort
// treatment of the ADCs and the shutter. One of them deserves a second look when
// it appears: if the pwm-2chan overlay is missing, the library falls back to
// driving a dimmable port as plain on/off, and the dew heater silently becomes a
// switch. The log line says so, and Board.Dimmable then reports false — which is
// what the Switch's set path checks before refusing a fractional duty.
func (h *Hub) Open(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refs > 0 {
		h.refs++
		return nil
	}
	board, warnings, err := h.openBoard()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		log.Printf("asiair: %v", w)
	}
	h.board = board
	h.refs = 1
	return nil
}

// Close undoes one Open; the last one releases the board.
//
// Releasing the board switches ports 3 and 4 off (the GPIO lines revert). That is
// the correct behaviour at process teardown — a driver that has stopped running
// should not leave power rails it can no longer control — but it is emphatically
// NOT what an ASCOM Disconnect should do.
func (h *Hub) Close(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refs == 0 {
		return nil
	}
	h.refs--
	if h.refs > 0 {
		return nil
	}
	b := h.board
	h.board = nil
	if b == nil {
		return nil
	}
	return b.Close()
}

// Board returns the open board, or NotConnected before Open / after Close.
func (h *Hub) Board() (*asiair.Board, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.board == nil {
		return nil, alpacadev.ErrNotConnected
	}
	return h.board, nil
}

// Ready reports whether the board is open.
func (h *Hub) Ready() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.board != nil
}
