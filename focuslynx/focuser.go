package driver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/optec-astro/focuslynx"
)

var _ alpacadev.Focuser = (*OptecFocuser)(nil)

// OptecFocuser adapts one channel of an optec-astro/focuslynx hub to the
// alpacadev.Focuser + Hardware interfaces. FocusLynx is a two-channel hub (F1/F2);
// ThirdLynx is single-channel (F1). One Alpaca device = one channel; run a second
// instance with -channel 2 for a FocusLynx's second port.
type OptecFocuser struct {
	alpacadev.BaseFocuser

	index    int
	ch       int
	maxStep  int
	nickname string // protocol identity to bind to; "" = bind by enumeration index

	mu  sync.Mutex
	hub *focuslynx.Hub // open handle; nil when no hub is attached

	// openDev acquires a hub and reports the channel to drive (discovered when
	// binding by nickname; the configured channel in index mode).
	openDev func() (*focuslynx.Hub, int, error)
}

// NewOptecFocuser creates the driver bound to a hub by enumeration index and a fixed
// channel (1 or 2). The binding follows plug order — prefer NewOptecFocuserByNickname
// for a stable identity.
func NewOptecFocuser(index, ch int) *OptecFocuser {
	f := &OptecFocuser{index: index, ch: ch}
	f.Version = "0.1.0"
	f.Info = "focuslynx — Optec FocusLynx / ThirdLynx Alpaca driver over pure-Go optec-astro/focuslynx"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = fmt.Sprintf("FocusLynx-foc%d-ch%d", index, ch)
	f.DevName = fmt.Sprintf("Optec FocusLynx F%d", ch)
	f.openDev = f.openByIndex
	return f
}

// NewOptecFocuserByNickname binds the Alpaca device (number devNum, used only for the
// stable ID) to the focuser whose protocol nickname is nick. The nickname is read over
// the serial link, so the binding is plug-order- and platform-independent (no OS
// USB-descriptor access), and it resolves which hub/channel to drive at connect time.
func NewOptecFocuserByNickname(devNum int, nick string) *OptecFocuser {
	f := &OptecFocuser{index: devNum, ch: 1, nickname: nick}
	f.Version = "0.1.0"
	f.Info = "focuslynx — Optec FocusLynx / ThirdLynx Alpaca driver over pure-Go optec-astro/focuslynx"
	f.IfaceVer = alpacadev.InterfaceVersionFocuser
	f.ID = "FocusLynx-" + nick
	f.DevName = "Optec FocusLynx " + nick
	f.openDev = f.openByNickname
	return f
}

func (f *OptecFocuser) Absolute() bool    { return true }
func (f *OptecFocuser) MaxStep() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }
func (f *OptecFocuser) MaxIncrement() int { f.mu.Lock(); defer f.mu.Unlock(); return f.maxStep }

// foc returns the channel controller for the open hub (caller holds mu, hub != nil).
func (f *OptecFocuser) foc() *focuslynx.Focuser { return f.hub.Focuser(f.ch) }

// --- Hardware lifecycle ---

func (f *OptecFocuser) Open(ctx context.Context) error {
	if f.openDev == nil {
		f.openDev = f.openByIndex
	}
	go alpacadev.Supervise(ctx, f.ID, func() { f.manageHardware(ctx) })
	return nil
}

func (f *OptecFocuser) Close(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub != nil {
		f.hub.Close()
		f.hub = nil
	}
	return nil
}

func (f *OptecFocuser) Connect(ctx context.Context) error {
	if !f.Connected() {
		return alpacadev.ErrNotConnected
	}
	return nil
}

func (f *OptecFocuser) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hub != nil
}

func (f *OptecFocuser) Disconnect(ctx context.Context) error { return nil }
func (f *OptecFocuser) Busy() bool                           { return f.IsMoving() }

func (f *OptecFocuser) manageHardware(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		present := f.hub != nil
		f.mu.Unlock()
		if !present {
			if f.tryAcquire() {
				log.Printf("focuslynx: focuser %s acquired", f.ID)
			} else {
				sleepCtx(ctx, 3*time.Second)
			}
			continue
		}
		f.mu.Lock()
		var err error
		if f.hub != nil {
			_, err = f.foc().Position()
		}
		if err != nil {
			log.Printf("focuslynx: focuser %s lost (%v); re-acquiring", f.ID, err)
			f.hub.Close()
			f.hub = nil
		}
		f.mu.Unlock()
		sleepCtx(ctx, 2*time.Second)
	}
}

func (f *OptecFocuser) tryAcquire() bool {
	hub, ch, err := f.openDev()
	if err != nil {
		return false
	}
	maxStep, _ := hub.Focuser(ch).MaxStep()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hub = hub
	f.ch = ch
	if maxStep > 0 {
		f.maxStep = maxStep
	}
	f.Desc = fmt.Sprintf("Optec FocusLynx channel F%d (max %d steps)", f.ch, f.maxStep)
	return true
}

func (f *OptecFocuser) openByIndex() (*focuslynx.Hub, int, error) {
	devs, err := focuslynx.Enumerate()
	if err != nil {
		return nil, 0, err
	}
	if f.index < 0 || f.index >= len(devs) {
		return nil, 0, fmt.Errorf("no FocusLynx hub at index %d (found %d)", f.index, len(devs))
	}
	hub, err := focuslynx.OpenPort(devs[f.index].Port)
	if err != nil {
		return nil, 0, err
	}
	return hub, f.ch, nil
}

// openByNickname binds to the focuser advertising f.nickname, discovering its
// hub/channel over the protocol.
func (f *OptecFocuser) openByNickname() (*focuslynx.Hub, int, error) {
	return focuslynx.OpenByNickname(f.nickname)
}

// --- Focuser members ---

func (f *OptecFocuser) IsMoving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return false
	}
	moving, err := f.foc().IsMoving()
	return err == nil && moving
}

func (f *OptecFocuser) Position() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.foc().Position()
}

func (f *OptecFocuser) Temperature() (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return 0, alpacadev.ErrNotConnected
	}
	return f.foc().Temperature()
}

// TempCompAvailable reports whether temperature compensation can be used — true only
// when a temperature probe is attached (status TmpProbe=1).
func (f *OptecFocuser) TempCompAvailable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return false
	}
	st, err := f.foc().Status()
	return err == nil && strings.Contains(st["TmpProbe"], "1")
}

// TempComp reports whether temperature compensation is currently enabled (config
// TComp ON=1).
func (f *OptecFocuser) TempComp() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return false
	}
	cfg, err := f.foc().Config()
	return err == nil && strings.Contains(cfg["TComp ON"], "1")
}

// SetTempComp enables or disables temperature compensation on this channel (SCTE).
func (f *OptecFocuser) SetTempComp(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return alpacadev.ErrNotConnected
	}
	return f.foc().SetTempComp(on)
}

func (f *OptecFocuser) Move(position int) error {
	if position < 0 || (f.MaxStep() > 0 && position > f.MaxStep()) {
		return alpacadev.ErrInvalidValue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return alpacadev.ErrNotConnected
	}
	return f.foc().MoveTo(position)
}

func (f *OptecFocuser) Halt() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub == nil {
		return alpacadev.ErrNotConnected
	}
	return f.foc().Halt()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
