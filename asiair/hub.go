// Package driver exposes the ASIAIR power board as an ASCOM Alpaca Switch.
package driver

import (
	"context"
	"log"
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/asiair"
)

// Hub reference-counts the board’s hardware lifetime.
// Releasing GPIO requests can cut port power, so client disconnects retain the board.
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

// Open acquires the board on the first call and increments its reference count.
// Nonfatal hardware warnings are logged.
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
