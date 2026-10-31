// The ASCOM Alpaca Switch driver for the scope_hub USB hub controller.
package driver

import (
	"context"
	"errors"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

// statusMaxAge bounds how stale a cached status may be. A client polling every
// slot reads one HID status report per interval instead of one per slot.
const statusMaxAge = 200 * time.Millisecond

// board owns the HID handle and a short-lived status cache. It reopens the
// device after a failure, so a replugged or rebooted board recovers on the next
// request.
type board struct {
	serial string // empty selects the first board

	mu       sync.Mutex
	dev      device
	snap     *snapshot
	statusAt time.Time

	// open is scopehub.Open; tests replace it.
	open func(serial string) (device, error)
}

// snapshot is one read of the board: its status and dew controller state.
type snapshot struct {
	st     *scopehub.Status
	dew    *scopehub.Dew
	aux    *scopehub.Aux
	labels *scopehub.Labels
}

// device is the part of *scopehub.Device the driver uses.
type device interface {
	Info() scopehub.Info
	Status() (*scopehub.Status, error)
	Dew() (*scopehub.Dew, error)
	SetDCAuto(port int, on bool) error
	SetDewSettings(s scopehub.DewSettings) error
	Aux() (*scopehub.Aux, error)
	SetLEDAuto(on bool) error
	Save() error
	Labels() (scopehub.Labels, error)
	SetDCLabel(port int, name string) error
	SetUSBLabel(port int, name string) error
	Defaults() error
	SetDC(port, duty int) error
	SetDCEnabled(port int, on bool) error
	SetDCPWM(port int, on bool) error
	SetUSB(port int, enabled bool) error
	SetLEDBrightness(pct int) error
	SetDCPWMHz(hz float64) (float64, error)
	Close() error
}

func newBoard(serial string) *board {
	return &board{serial: serial, open: func(s string) (device, error) { return scopehub.Open(s) }}
}

var _ device = (*scopehub.Device)(nil)

func (b *board) ensure() (device, error) {
	if b.dev != nil {
		return b.dev, nil
	}
	d, err := b.open(b.serial)
	if err != nil {
		return nil, alpacadev.NewError(alpacadev.ErrNumNotConnected, "scope_hub board not available: "+err.Error())
	}
	b.dev = d
	return d, nil
}

// drop forgets a failed handle so the next request reopens the board.
func (b *board) drop() {
	if b.dev != nil {
		b.dev.Close()
		b.dev = nil
	}
	b.snap = nil
}

// Open acquires the board. A missing board is not fatal: requests retry.
func (b *board) Open(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.ensure()
	return err
}

// Close releases the board.
func (b *board) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.drop()
	return nil
}

// Snapshot returns a reading no older than statusMaxAge.
func (b *board) Snapshot() (*snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.snap != nil && time.Since(b.statusAt) < statusMaxAge {
		return b.snap, nil
	}
	d, err := b.ensure()
	if err != nil {
		return nil, err
	}
	s, err := d.Status()
	if err == nil {
		var w *scopehub.Dew
		var a *scopehub.Aux
		if w, err = d.Dew(); err == nil {
			if a, err = d.Aux(); err == nil {
				var l scopehub.Labels
				if l, err = d.Labels(); err == nil {
					b.snap, b.statusAt = &snapshot{st: s, dew: w, aux: a, labels: &l}, time.Now()
					return b.snap, nil
				}
			}
		}
	}
	b.drop()
	return nil, err
}

// Do runs one command and invalidates the cache so the next read shows its effect.
func (b *board) Do(fn func(d device) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, err := b.ensure()
	if err != nil {
		return err
	}
	b.snap = nil
	if err := fn(d); err != nil {
		// A rejected value or a failed I2C write leaves the HID link healthy.
		if !errors.Is(err, scopehub.ErrOutOfRange) && !errors.Is(err, scopehub.ErrCommandFailed) {
			b.drop()
		}
		return err
	}
	return nil
}

// Info returns the board identity, opening it if needed.
func (b *board) Info() (scopehub.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, err := b.ensure()
	if err != nil {
		return scopehub.Info{}, err
	}
	return d.Info(), nil
}
