// ASCOM Alpaca Switch and ObservingConditions driver for scope_hub USB hub controller.
package driver

import (
	"context"
	"errors"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

// statusMaxAge bounds cached status age. Client polling every slot costs one HID
// status report per interval, not one per slot.
const statusMaxAge = 200 * time.Millisecond

// board owns HID handle and short-lived status cache. Reopens device after
// failure, so replugged or rebooted board recovers on next request. Switch and
// ObservingConditions of one serial share one board per process (see share).
type board struct {
	serial string // empty selects the first board
	refs   int    // devices holding shared board; boardsMu guards

	mu   sync.Mutex
	dev  device
	snap *snapshot

	// open is scopehub.Open; tests replace it.
	open func(serial string) (device, error)
}

// snapshot is one board read: status and dew controller state.
type snapshot struct {
	st     *scopehub.Status
	dew    *scopehub.Dew
	aux    *scopehub.Aux
	labels *scopehub.Labels
	at     time.Time // when read
}

// device is the part of *scopehub.Device driver uses.
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

// boards maps serial to shared board while any device holds it.
var (
	boardsMu sync.Mutex
	boards   = map[string]*board{}
)

// share returns process board for b's serial and takes one reference.
// None held: b itself becomes shared board, open hook included.
func (b *board) share() *board {
	boardsMu.Lock()
	defer boardsMu.Unlock()
	if s := boards[b.serial]; s != nil {
		s.refs++
		return s
	}
	b.refs = 1
	boards[b.serial] = b
	return b
}

// release drops one reference. Last one closes HID device.
func (b *board) release() {
	boardsMu.Lock()
	defer boardsMu.Unlock()
	if b.refs--; b.refs > 0 {
		return
	}
	if boards[b.serial] == b {
		delete(boards, b.serial)
	}
	b.Close(context.Background())
}

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

// drop forgets failed handle so next request reopens board.
func (b *board) drop() {
	if b.dev != nil {
		b.dev.Close()
		b.dev = nil
	}
	b.snap = nil
}

// Open acquires board. Missing board not fatal; requests retry.
func (b *board) Open(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.ensure()
	return err
}

// Close releases board.
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
	if b.snap != nil && time.Since(b.snap.at) < statusMaxAge {
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
					b.snap = &snapshot{st: s, dew: w, aux: a, labels: &l, at: time.Now()}
					return b.snap, nil
				}
			}
		}
	}
	b.drop()
	return nil, err
}

// present reports hardware answering: a status read no older than statusMaxAge. A cached
// handle alone is not enough; an unplugged board fails the read and drops the handle.
func (b *board) present() error {
	_, err := b.Snapshot()
	return err
}

// Do runs one command and drops cache so next read shows effect.
func (b *board) Do(fn func(d device) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, err := b.ensure()
	if err != nil {
		return err
	}
	b.snap = nil
	if err := fn(d); err != nil {
		// Rejected value or failed I2C write leaves HID link healthy.
		if !errors.Is(err, scopehub.ErrOutOfRange) && !errors.Is(err, scopehub.ErrCommandFailed) {
			b.drop()
		}
		return err
	}
	return nil
}

// Info returns board identity, opening board if needed.
func (b *board) Info() (scopehub.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, err := b.ensure()
	if err != nil {
		return scopehub.Info{}, err
	}
	return d.Info(), nil
}
