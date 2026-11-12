package driver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

const pollInterval = 500 * time.Millisecond
const staleAfter = 5 * time.Second
const lightTimeout = 5 * time.Second
const motionTimeout = 90 * time.Second

type snapshot struct {
	brightness int
	on         bool
	cover      alpacadev.CoverStatus
	at         time.Time
}

type lightChange struct {
	brightness      int
	on              bool
	waitingForCover bool
	deadline        time.Time
}

type Panel struct {
	alpacadev.BaseCoverCalibrator
	mu            sync.Mutex
	port          link
	product       int
	firmware      string
	state         snapshot
	light         *lightChange
	lightError    bool
	coverError    bool
	coverTarget   alpacadev.CoverStatus
	coverDeadline time.Time
	stop          func(time.Duration)
	open          func() (link, error)
	now           func() time.Time
}

var _ alpacadev.CoverCalibrator = (*Panel)(nil)

func NewPanel(cfg Config) *Panel {
	p := &Panel{now: time.Now}
	p.ID = "deepskydadfp-" + cfg.Serial
	if cfg.Serial == "" {
		p.ID = "deepskydadfp-" + cfg.Path
	}
	p.DevName = "Deep Sky Dad flat panel"
	p.Desc = "FP2-family USB flat panel and cover"
	p.Info = "Native driver for Deep Sky Dad FP2-family panels"
	p.Version = "0.1.0"
	p.IfaceVer = alpacadev.InterfaceVersionCoverCalibrator
	p.open = func() (link, error) { return openPanel(cfg) }
	return p
}

func (p *Panel) Open(ctx context.Context) error {
	p.stop = alpacadev.RunLoop(ctx, p.ID, p.manage)
	return nil
}
func (p *Panel) Close(context.Context) error {
	if p.stop != nil {
		p.stop(15 * time.Second)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked()
	return nil
}
func (p *Panel) dropLocked() {
	if p.port != nil {
		p.port.Close()
		p.port = nil
	}
	p.state.at = time.Time{}
	p.MarkDisconnected()
	if p.light != nil {
		p.lightError = true
		p.light = nil
	}
	if !p.coverDeadline.IsZero() {
		p.coverError = true
		p.coverDeadline = time.Time{}
	}
}
func (p *Panel) freshLocked() bool {
	return p.port != nil && !p.state.at.IsZero() && p.now().Sub(p.state.at) < staleAfter
}

// connectedLocked means hardware held with fresh readings, not a client flag: a replug or driver
// restart reports connected once the manager reacquires the panel, without a client Connect.
func (p *Panel) connectedLocked() bool { return p.freshLocked() }
func (p *Panel) Connected() bool       { p.mu.Lock(); defer p.mu.Unlock(); return p.connectedLocked() }

// Hardware is acquired by the manager. Connect only checks it is held; Disconnect
// leaves it held. Both complete immediately, so Connecting stays false.
func (p *Panel) Connect(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.freshLocked() {
		return alpacadev.ErrNotConnected
	}
	return nil
}
func (p *Panel) Disconnect(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Cancel deferred work; never turn the light on after a client disconnects.
	if p.light != nil && p.light.waitingForCover {
		p.light = nil
	}
	return nil
}

func (p *Panel) acquireLocked() error {
	port, err := p.open()
	if err != nil {
		return err
	}
	firmware, product, err := identify(port)
	if err != nil {
		port.Close()
		return err
	}
	p.port, p.product, p.firmware = port, product, firmware
	if err = p.refreshLocked(); err != nil {
		p.dropLocked()
		return err
	}
	transport := "serial"
	if _, isHID := port.(*hidLink); isHID {
		transport = "HID"
	}
	log.Printf("deepskydadfp: %s acquired product %d, firmware %s, over %s", p.ID, product, firmware, transport)
	return nil
}
func (p *Panel) refreshLocked() (err error) {
	defer func() {
		if err != nil {
			if p.product != 5 {
				p.lightError = true
			}
			if p.product != 2 {
				p.coverError = true
			}
			p.dropLocked()
		}
	}()
	next := snapshot{cover: alpacadev.CoverNotPresent}
	if p.product != 2 {
		moving, err := number(p.port, "GMOV", 0, 1)
		if err != nil {
			return err
		}
		position, err := number(p.port, "GOPS", 0, 3)
		if err != nil {
			return err
		}
		switch {
		case moving == 1 || position == 2:
			next.cover = alpacadev.CoverMoving
		case position == 0:
			next.cover = alpacadev.CoverClosed
		case position == 1:
			next.cover = alpacadev.CoverOpen
		default:
			next.cover = alpacadev.CoverUnknown
		}
	}
	if p.product != 5 {
		on, err := number(p.port, "GLON", 0, 1)
		if err != nil {
			return err
		}
		next.on = on == 1
		brightness, err := number(p.port, "GLBR", 0, maxBrightness)
		if err != nil {
			return err
		}
		next.brightness = brightness
	}
	next.at = p.now()
	p.state = next
	if !p.coverDeadline.IsZero() {
		switch {
		case next.cover == p.coverTarget:
			p.coverDeadline = time.Time{}
		case next.cover != alpacadev.CoverMoving || !next.at.Before(p.coverDeadline):
			log.Printf("deepskydadfp: %s: cover failed to reach %v before stopping or timing out", p.ID, p.coverTarget)
			p.coverError = true
			p.coverDeadline = time.Time{}
		}
	}
	if change := p.light; change != nil {
		if change.waitingForCover {
			if p.coverError {
				p.lightError = true
				p.light = nil
			} else if next.cover == alpacadev.CoverClosed {
				// Only send once, after confirmed closure. An ambiguous write is never retried.
				change.waitingForCover = false
				change.deadline = p.now().Add(lightTimeout)
				if err := p.writeLightLocked(change.brightness, change.on); err != nil {
					return err
				}
			}
		} else if next.on == change.on && (!change.on || next.brightness == change.brightness) {
			p.light = nil
		} else if !next.at.Before(change.deadline) {
			log.Printf("deepskydadfp: %s: light did not confirm on=%v brightness=%d within %s", p.ID, change.on, change.brightness, lightTimeout)
			p.lightError = true
			p.light = nil
		}
	}
	return nil
}
func (p *Panel) manage(ctx context.Context) {
	lastError := ""
	for ctx.Err() == nil {
		p.mu.Lock()
		acquiring := p.port == nil
		var err error
		if acquiring {
			err = p.acquireLocked()
		} else {
			err = p.refreshLocked()
		}
		if err != nil {
			p.dropLocked()
		}
		p.mu.Unlock()
		delay := pollInterval
		if err != nil {
			delay = 3 * time.Second
			if err.Error() != lastError {
				log.Printf("deepskydadfp: %s: %v", p.ID, err)
			}
			lastError = err.Error()
		}
		if err == nil {
			lastError = ""
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Panel) Brightness() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.freshLocked() || !p.state.on {
		return 0
	}
	return p.state.brightness
}
func (p *Panel) MaxBrightness() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.product == 5 {
		return 0
	}
	return maxBrightness
}
func (p *Panel) CalibratorState() alpacadev.CalibratorStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.product == 5 {
		return alpacadev.CalibratorNotPresent
	}
	if p.lightError {
		return alpacadev.CalibratorError
	}
	if p.light != nil {
		return alpacadev.CalibratorNotReady
	}
	if !p.freshLocked() {
		return alpacadev.CalibratorUnknown
	}
	if !p.state.on {
		return alpacadev.CalibratorOff
	}
	return alpacadev.CalibratorReady
}
func (p *Panel) CoverState() alpacadev.CoverStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.product == 2 {
		return alpacadev.CoverNotPresent
	}
	if p.coverError {
		return alpacadev.CoverError
	}
	if !p.freshLocked() {
		return alpacadev.CoverUnknown
	}
	return p.state.cover
}
func (p *Panel) CoverMoving() bool        { return p.CoverState() == alpacadev.CoverMoving }
func (p *Panel) CalibratorChanging() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.light != nil }
func (p *Panel) Busy() bool               { return p.CoverMoving() || p.CalibratorChanging() }

// Call with mu held. No command is retried after an ambiguous reply. Rejection
// preserves a synchronized link but invalidates cached state for the next poll.
func (p *Panel) ackLocked(command string) error {
	err := acknowledge(p.port, command)
	if err != nil {
		if isRejected(err) {
			p.state.at = time.Time{}
		} else {
			p.dropLocked()
		}
		return alpacadev.NewError(alpacadev.ErrNumDriverBase, err.Error())
	}
	return nil
}

// The firmware interlocks illumination with cover closure. ASCOM permits
// CalibratorOn to close the cover; completion includes closure and light readback.
func (p *Panel) CalibratorOn(brightness int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return alpacadev.ErrNotConnected
	}
	if p.product == 5 {
		return alpacadev.ErrNotImplemented
	}
	if brightness < 0 || brightness > maxBrightness {
		return alpacadev.ErrInvalidValue
	}
	p.lightError = false
	p.light = &lightChange{brightness: brightness, on: true, deadline: p.now().Add(lightTimeout)}
	if p.product != 2 && p.state.cover != alpacadev.CoverClosed {
		p.light.waitingForCover = true
		if !p.coverDeadline.IsZero() && p.coverTarget == alpacadev.CoverClosed {
			return nil
		}
		if p.state.cover == alpacadev.CoverMoving {
			p.light = nil
			return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "wait for cover motion before enabling the light")
		}
		if err := p.moveLocked(270); err != nil {
			p.light = nil
			p.lightError = true
			return err
		}
		return nil
	}
	if err := p.writeLightLocked(brightness, true); err != nil {
		p.light = nil
		p.lightError = true
		return err
	}
	return nil
}
func (p *Panel) writeLightLocked(brightness int, on bool) error {
	if on {
		if err := p.ackLocked(fmt.Sprintf("SLBR%04d", brightness)); err != nil {
			return err
		}
		return p.ackLocked("SLON1")
	}
	return p.ackLocked("SLON0")
}
func (p *Panel) CalibratorOff() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return alpacadev.ErrNotConnected
	}
	if p.product == 5 {
		return alpacadev.ErrNotImplemented
	}
	p.lightError = false
	p.light = &lightChange{deadline: p.now().Add(lightTimeout)}
	if err := p.writeLightLocked(0, false); err != nil {
		p.light = nil
		p.lightError = true
		return err
	}
	return nil
}
func (p *Panel) move(target int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return alpacadev.ErrNotConnected
	}
	if p.product == 2 {
		return alpacadev.ErrNotImplemented
	}
	if p.light != nil && p.light.waitingForCover {
		return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "cover is closing for illumination; turn the calibrator off or halt first")
	}
	return p.moveLocked(target)
}
func (p *Panel) moveLocked(target int) error {
	if p.state.cover == alpacadev.CoverMoving {
		return alpacadev.NewError(alpacadev.ErrNumInvalidOperation, "cover already moving")
	}
	p.coverError = false
	if err := p.ackLocked(fmt.Sprintf("STRG%d", target)); err != nil {
		p.coverError = true
		return err
	}
	if err := p.ackLocked("SMOV"); err != nil {
		p.coverError = true
		return err
	}
	p.coverTarget = alpacadev.CoverOpen
	if target == 270 {
		p.coverTarget = alpacadev.CoverClosed
	}
	p.coverDeadline = p.now().Add(motionTimeout)
	p.state.cover = alpacadev.CoverMoving
	return nil
}
func (p *Panel) OpenCover() error  { return p.move(0) }
func (p *Panel) CloseCover() error { return p.move(270) }
func (p *Panel) HaltCover() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.connectedLocked() {
		return alpacadev.ErrNotConnected
	}
	if p.product == 2 {
		return alpacadev.ErrNotImplemented
	}
	if p.light != nil && p.light.waitingForCover {
		p.light = nil
		p.lightError = true
	}
	if err := p.ackLocked("STOP"); err != nil {
		p.coverError = true
		return err
	}
	p.coverDeadline = time.Time{}
	p.coverError = false
	p.state.cover = alpacadev.CoverUnknown
	return nil
}
