package driver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/pegasus-astro/focuscube"
)

var _ alpacadev.Focuser = (*PegasusFocuser)(nil)

// PegasusFocuser adapts a pegasus-astro/focuscube focuser to the alpacadev.Focuser +
// Hardware interfaces. The FocusCube does not report a travel limit, so MaxStep is
// configured at startup. TempCompAvailable is false: the serial protocol has no
// on-device temp-comp command (compensation is host-side).
type PegasusFocuser struct {
	alpacadev.BaseFocuser

	index   int
	serial  string // USB serial to bind to; "" = bind by enumeration index
	maxStep int

	mu  sync.Mutex
	dev *focuscube.FocusCube // open handle; nil when no focuser is attached

	openDev func() (*focuscube.FocusCube, error)
}

// NewPegasusFocuser creates the driver for the FocusCube at the given enumeration
// index, with the host-configured maximum step. Binding follows plug order; prefer
// NewPegasusFocuserBySerial for a stable identity.
func NewPegasusFocuser(index, maxStep int) *PegasusFocuser {
	f := &PegasusFocuser{index: index, maxStep: maxStep}
	f.Version = "0.1.0"
	f.Info = "focuscube — Pegasus Astro FocusCube Alpaca driver over pure-Go pegasus-astro/focuscube"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = fmt.Sprintf("FocusCube-foc%d", index)
	f.DevName = fmt.Sprintf("Pegasus FocusCube %d", index)
	f.openDev = f.openByIndex
	return f
}

// NewPegasusFocuserBySerial binds the Alpaca device (devNum used only for the stable
// ID) to the FocusCube whose USB serial number is serial. The serial is read from the
// USB descriptor before the port opens, so the binding is plug-order- and
// platform-independent and disambiguates several FTDI devices sharing VID 0x0403.
func NewPegasusFocuserBySerial(devNum int, serial string, maxStep int) *PegasusFocuser {
	f := &PegasusFocuser{index: devNum, serial: serial, maxStep: maxStep}
	f.Version = "0.1.0"
	f.Info = "focuscube — Pegasus Astro FocusCube Alpaca driver over pure-Go pegasus-astro/focuscube"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = "FocusCube-" + serial
	f.DevName = "Pegasus FocusCube " + serial
	f.openDev = f.openBySerial
	return f
}

func (f *PegasusFocuser) Absolute() bool    { return true }
func (f *PegasusFocuser) MaxStep() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }
func (f *PegasusFocuser) MaxIncrement() int { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }

// --- Hardware lifecycle ---

func (f *PegasusFocuser) Open(ctx context.Context) error {
	if f.openDev == nil {
		f.openDev = f.openByIndex
	}
	go alpacadev.Supervise(ctx, f.ID, func() { f.manageHardware(ctx) })
	return nil
}

func (f *PegasusFocuser) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev != nil {
		f.dev.Halt()
		f.dev.Close()
		f.dev = nil
	}
	return nil
}

func (f *PegasusFocuser) Connect(ctx context.Context) error {
	if !f.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (f *PegasusFocuser) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dev != nil
}

func (f *PegasusFocuser) Disconnect(ctx context.Context) error { return nil }
func (f *PegasusFocuser) Busy() bool                           { return f.IsMoving() }

func (f *PegasusFocuser) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		present := f.dev != nil
		f.mu.Unlock()
		if !present {
			if f.tryAcquire() {
				log.Printf("focuscube: focuser %s acquired", f.ID)
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
			log.Printf("focuscube: focuser %s lost (%v); re-acquiring", f.ID, err)
			f.dev.Close()
			f.dev = nil
		}
		f.mu.Unlock()
		sleepCtx(ctx, 2*time.Second)
	}
}

func (f *PegasusFocuser) tryAcquire() bool {
	dev, err := f.openDev()
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dev = dev
	f.Desc = fmt.Sprintf("Pegasus FocusCube (FTDI serial, max %d steps)", f.maxStep)
	return true
}

func (f *PegasusFocuser) openByIndex() (*focuscube.FocusCube, error) {
	devs, err := focuscube.Enumerate()
	if err != nil {
		return nil, err
	}
	if f.index < 0 || f.index >= len(devs) {
		return nil, fmt.Errorf("no FocusCube at index %d (found %d)", f.index, len(devs))
	}
	return focuscube.OpenPort(devs[f.index].Port)
}

// openBySerial binds to the FocusCube whose USB serial matches f.serial.
func (f *PegasusFocuser) openBySerial() (*focuscube.FocusCube, error) {
	return focuscube.OpenBySerial(f.serial)
}

// --- Focuser members ---

func (f *PegasusFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return false
	}
	moving, err := f.dev.IsMoving()
	return err == nil && moving
}

func (f *PegasusFocuser) Position() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.dev.Position()
}

func (f *PegasusFocuser) Temperature() (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.dev.Temperature()
}

func (f *PegasusFocuser) Move(position int) error {
	if position < 0 || (f.MaxStep() > 0 && position > f.MaxStep()) {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.MoveTo(position)
}

func (f *PegasusFocuser) Halt() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dev == nil {
		return alpacadev.ErrNotConnected
	}
	return f.dev.Halt()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
