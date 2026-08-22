// Package driver is the ASCOM Alpaca front-end for the StellarMate SM Pro CM5
// controller board, over the pure-Go stellarmate library. The board carries two
// ASCOM device types — a Switch (power outputs, dew heaters, variable output,
// LEDs, antenna power, voltage/current sensing) and a Focuser (the TMC2209
// stepper) — which share one board handle through a Hub. Served standalone by
// cmd/smpro and hosted by the alpacahurd aggregator.
package driver

import (
	"context"
	"log"
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// Hub owns the stellarmate.Board an Alpaca device drives, over the subsystems
// that device needs. The board's two device types drive disjoint hardware — the
// Switch has the I2C expander, the DAC, the SPI ADC and the dew PWM, the Focuser
// has the TMC2209 on its own UART — so each opens only its own and the two never
// contend, whether they run in one process or two. Open/Close are refcounted
// because the goalpaca server runs the Hardware lifecycle once per registered
// device.
type Hub struct {
	mu    sync.Mutex
	board *stellarmate.Board
	refs  int

	// openBoard opens the hardware; a test replaces it to inject a Board over
	// the stellarmate bus fakes.
	openBoard func() (*stellarmate.Board, []error, error)
}

// NewHub creates a board owner for the given config, opening only subs.
// stellarmate.SubsystemsSwitch and SubsystemsFocuser are the two device sets;
// SubsystemsAll opens the whole board, which is what a binary serving both from
// one handle wants.
func NewHub(cfg stellarmate.Config, subs stellarmate.Subsystem) *Hub {
	return &Hub{openBoard: func() (*stellarmate.Board, []error, error) {
		return stellarmate.OpenSubsystems(cfg, subs)
	}}
}

// Open opens the board on the first call and refcounts the rest. Warnings from
// optional subsystems that did not come up are logged, matching the library's
// best-effort treatment of the ADC/DAC/PWM/focuser.
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
		log.Printf("smpro: %v", w)
	}
	h.board = board
	h.refs = 1
	return nil
}

// Close undoes one Open; the last one releases the board.
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
func (h *Hub) Board() (*stellarmate.Board, error) {
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
