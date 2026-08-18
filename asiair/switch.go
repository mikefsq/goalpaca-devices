package driver

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/mikefsq/asiair"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

var _ alpacadev.Switch = (*AsiairSwitch)(nil)

// slot is one entry in the switch array: its ASCOM metadata and the Board
// accessors behind it. Boolean slots use min 0 / max 1 / step 1 (the ISwitchV3
// binary convention, Max−Min == Step); set is nil for read-only sensors.
type slot struct {
	name, desc     string
	min, max, step float64
	get            func(b *asiair.Board) (float64, error)
	set            func(b *asiair.Board, v float64) error
}

// b2f renders a boolean reading as the 0/1 analog view of a binary switch.
func b2f(v bool, err error) (float64, error) {
	if v {
		return 1, err
	}
	return 0, err
}

// Hardware limits, used as the static MinSwitchValue/MaxSwitchValue on the
// read-only sensor slots. They are derived from the ADC ranges and the board
// scaling rather than guessed: the voltage dividers are 21:1 on a ±1.024 V range
// (so ~21.5 V full scale), the port shunts are 12.5 mA/LSB on 12 signed bits
// (~25.6 A), and the main sense is 5 mA/LSB (~10.2 A).
const (
	maxVolts    = 22.0
	maxPortAmps = 26.0
	maxMainAmps = 11.0
)

// newSlots builds the switch array for a given board config.
//
// It is built per device rather than being a package-level var (as in smpro)
// because which ports can dim is a property of the Pi's pin functions and the
// applied overlay — see Config.PWMChannel. ISwitchV3 requires the metadata to be
// static, and it is: the config is fixed for the session. The array's LENGTH is
// also fixed at 17 across both pairings, so a saved client profile keyed on switch
// id survives a change of pairing.
//
// Order is part of the public surface once clients save profiles against it;
// append rather than reorder.
func newSlots(cfg asiair.Config) []slot {
	s := make([]slot, 0, 17)

	// 0..3 — the four power ports.
	//
	// A port that the config gives a PWM channel is advertised as a 0..100 duty
	// cycle; the rest are plain booleans. That difference is the Pi's silicon, not
	// a choice: the PWM peripheral reaches the header on two channels only, so at
	// most two of the four ports can dim, and GPIO 26 (Port 3) has no PWM
	// alt-function at all.
	for i := 0; i < asiair.NumPorts; i++ {
		p := asiair.Port(i)
		n := i + 1
		if cfg.PWMChannel[i] >= 0 {
			s = append(s, slot{
				name: fmt.Sprintf("Port %d", n),
				desc: fmt.Sprintf("Power port %d — duty cycle %%, dimmable (dew heater / flat panel / fan)", n),
				min:  0, max: 100, step: 1,
				get: func(b *asiair.Board) (float64, error) { return getDimmable(b, p) },
				set: func(b *asiair.Board, v float64) error { return setDimmable(b, p, v) },
			})
			continue
		}
		s = append(s, slot{
			name: fmt.Sprintf("Port %d", n),
			desc: fmt.Sprintf("Power port %d — on/off (no hardware PWM on this pin)", n),
			min:  0, max: 1, step: 1,
			get: func(b *asiair.Board) (float64, error) { return b2f(b.Port(p)) },
			set: func(b *asiair.Board, v float64) error { return b.SetPort(p, v >= 0.5) },
		})
	}

	// 4 — the DSLR shutter contact.
	//
	// This is a LEVEL, not a momentary. The ASCOM Switch FAQ does advise momentary
	// action on one edge of SetSwitch — but a DSLR bulb exposure requires the
	// contact to be HELD closed for the whole exposure, so a momentary switch could
	// not express what this line actually does. 1 = contact closed = exposing.
	// Timed runs (N frames × duration + gap) have no home in ISwitchV3 at all and
	// live on the Action seam; see actions.go.
	s = append(s, slot{
		name: "DSLR Shutter", desc: "DSLR shutter contact (1 = closed, exposing)",
		min: 0, max: 1, step: 1,
		get: func(b *asiair.Board) (float64, error) { return b2f(b.Shutter()) },
		set: func(b *asiair.Board, v float64) error { return b.SetShutter(v >= 0.5) },
	})

	// 5..6 — auto-dew enables, one per dimmable port. Ramp parameters and the
	// weather feed are on the Action interface (see actions.go).
	//
	// Exactly two slots either way, so MaxSwitch does not move between pairings —
	// but they are NAMED for the port they actually control, because under the
	// ports-2+4 pairing "Auto Dew 1" would otherwise be a lie.
	for i := 0; i < asiair.NumPorts; i++ {
		if cfg.PWMChannel[i] < 0 {
			continue
		}
		p := asiair.Port(i)
		n := i + 1
		s = append(s, slot{
			name: fmt.Sprintf("Auto Dew (Port %d)", n),
			desc: fmt.Sprintf("Auto-dew control for port %d — drives duty from the temperature/dew-point margin", n),
			min:  0, max: 1, step: 1,
			get: func(b *asiair.Board) (float64, error) {
				cfg, err := b.AutoDew(p)
				return b2f(cfg.Enabled, err)
			},
			set: func(b *asiair.Board, v float64) error { return setAutoDewEnabled(b, p, v >= 0.5) },
		})
	}

	// 7..8 — the main input (read-only; set == nil → CanWrite false).
	s = append(s,
		slot{name: "Input Voltage", desc: "Main input supply voltage (V, read-only)",
			min: 0, max: maxVolts, step: 0.01,
			get: func(b *asiair.Board) (float64, error) { return nonNeg(b.InputVolts()) }},
		slot{name: "Input Current", desc: "Total current drawn from the main input (A, read-only — scaling UNVERIFIED, see README)",
			min: 0, max: maxMainAmps, step: 0.001,
			get: func(b *asiair.Board) (float64, error) { return nonNeg(b.InputAmps()) }},
	)

	// 9..16 — per-port voltage and current (read-only).
	for i := 0; i < asiair.NumPorts; i++ {
		p := asiair.Port(i)
		n := i + 1
		s = append(s, slot{
			name: fmt.Sprintf("Port %d Voltage", n),
			desc: fmt.Sprintf("Port %d output voltage (V, read-only)", n),
			min:  0, max: maxVolts, step: 0.01,
			get: func(b *asiair.Board) (float64, error) { return nonNeg(b.PortVolts(p)) },
		})
	}
	for i := 0; i < asiair.NumPorts; i++ {
		p := asiair.Port(i)
		n := i + 1
		s = append(s, slot{
			name: fmt.Sprintf("Port %d Current", n),
			desc: fmt.Sprintf("Port %d current draw (A, read-only)", n),
			min:  0, max: maxPortAmps, step: 0.001,
			get: func(b *asiair.Board) (float64, error) { return nonNeg(b.PortAmps(p)) },
		})
	}

	return s
}

// nonNeg clamps a sensor reading to zero.
//
// The ADS1015 result is signed, and a current shunt at zero load sits a hair
// below zero — a real front end dithers a millivolt either side from offset and
// noise, so an idle port genuinely reads about −12 mA. That is the truth, and the
// library reports it. But ISwitchV3 requires GetSwitchValue to fall within
// MinSwitchValue..MaxSwitchValue, and a MinSwitchValue of −0.05 A on a current
// sensor is a worse lie to a client UI than clamping the noise away. So: clamp
// here, at the ASCOM boundary, and leave the library honest.
func nonNeg(v float64, err error) (float64, error) {
	if v < 0 {
		v = 0
	}
	return v, err
}

// getDimmable reads a port advertised as a 0..100 duty.
//
// If the hardware fell back to on/off (no overlay), report the extremes rather
// than an error: the port really is on or off, and 0/100 is the honest projection
// of that onto the advertised scale.
func getDimmable(b *asiair.Board, p asiair.Port) (float64, error) {
	if b.Dimmable(p) {
		d, err := b.PortDuty(p)
		return float64(d), err
	}
	on, err := b.Port(p)
	if on {
		return 100, err
	}
	return 0, err
}

// setDimmable writes a port advertised as a 0..100 duty.
//
// The awkward case is a port the config says is dimmable but the hardware is not,
// because the pwm-2chan overlay was never applied and Open fell back to a plain
// GPIO line. Then 0 and 100 still work — off and on — and only a fractional duty
// is genuinely impossible.
//
// So refuse only the fractional case, and say why. Refusing 0 and 100 as well
// would leave the port unusable through this slot entirely, which is a bad answer
// for a camera. Silently rounding 60% up to full power is a worse one: the user
// would never learn that their dew heater has no PWM, and would spend a season
// wondering why it only ever runs flat out.
func setDimmable(b *asiair.Board, p asiair.Port, v float64) error {
	pct := int(math.Round(v))
	if b.Dimmable(p) {
		return b.SetPortDuty(p, pct)
	}
	switch pct {
	case 0:
		return b.SetPort(p, false)
	case 100:
		return b.SetPort(p, true)
	}
	return alpacadev.NewError(alpacadev.ErrNumInvalidValue, fmt.Sprintf(
		"port %d has no hardware PWM: only 0 or 100 work. The pwm-2chan overlay is missing — add "+
			"'dtoverlay=pwm-2chan,pin=12,func=4,pin2=13,func2=4' to config.txt and reboot",
		int(p)+1))
}

func setAutoDewEnabled(b *asiair.Board, p asiair.Port, on bool) error {
	cfg, err := b.AutoDew(p)
	if err != nil {
		return err
	}
	cfg.Enabled = on
	return b.SetAutoDew(p, cfg)
}

// AsiairSwitch is the ASCOM Switch device over the ASIAIR power board.
type AsiairSwitch struct {
	alpacadev.BaseSwitch
	hub   *Hub
	slots []slot

	mu sync.Mutex
	// connected is the ASCOM connection state, which is NOT the same thing as the
	// board being open. See Disconnect.
	connected bool
}

// NewSwitch creates the Switch device on a Hub.
func NewSwitch(hub *Hub) *AsiairSwitch {
	s := &AsiairSwitch{hub: hub, slots: newSlots(hub.Config())}
	s.Version = "0.1.0"
	s.Info = "asiair — ZWO ASIAIR power/dew/DSLR Alpaca driver over Go asiair"
	s.IfaceVer = alpacadev.InterfaceVersionSwitch
	s.ID = "ASIAIR-switch"
	s.DevName = "ASIAIR Power"
	s.Desc = "ASIAIR power board: four ports (two dimmable), DSLR shutter, per-port voltage and current"
	return s
}

// --- Lifecycle ---
//
// Two different things are going on here, and conflating them is the mistake this
// driver most wants to avoid.
//
//   - The BOARD is hardware. It is opened once at Hardware startup (Open) and
//     released once at Hardware teardown (Close). It is owned by the PROCESS.
//   - CONNECTED is an ASCOM client's logical session. Connect and Disconnect flip
//     it, and operational members fault with NotConnected while it is false. It is
//     owned by the CLIENT.
//
// ASCOM requires Connected to go false after Disconnect. It does NOT require the
// hardware to be released — and here it must not be, because the on/off ports are
// held by GPIO character-device line requests that the kernel reverts the moment
// their fd closes. Tying the board's lifetime to the client's would mean that any
// client disconnecting mid-session cuts power to whatever is on ports 3 and 4:
// typically the mount, which loses its position, and the camera, which loses its
// cooling.
//
// So Disconnect drops the session and keeps the power on. Both halves matter.

func (s *AsiairSwitch) Open(ctx context.Context) error { return s.hub.Open(ctx) }

func (s *AsiairSwitch) Close(ctx context.Context) error {
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	return s.hub.Close(ctx)
}

func (s *AsiairSwitch) Connect(ctx context.Context) error {
	if !s.hub.Ready() {
		return alpacadev.ErrNotConnected
	}
	s.mu.Lock()
	s.connected = true
	s.mu.Unlock()
	return nil
}

func (s *AsiairSwitch) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Disconnect ends the ASCOM session. The board stays open and the ports stay
// powered — see the block comment above.
func (s *AsiairSwitch) Disconnect(ctx context.Context) error {
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	return nil
}

// board returns the board for an operational member: NotConnected unless the
// client has connected AND the hardware is open.
func (s *AsiairSwitch) board() (*asiair.Board, error) {
	if !s.Connected() {
		return nil, alpacadev.ErrNotConnected
	}
	return s.hub.Board()
}

// --- ISwitchV3 ---

func (s *AsiairSwitch) MaxSwitch() int { return len(s.slots) }

// slotAt is safe unchecked indexing: the server range-checks the Id against
// MaxSwitch before every call.
func (s *AsiairSwitch) slotAt(id int) *slot { return &s.slots[id] }

func (s *AsiairSwitch) CanWrite(id int) (bool, error) { return s.slotAt(id).set != nil, nil }

func (s *AsiairSwitch) GetSwitchName(id int) (string, error) { return s.slotAt(id).name, nil }

func (s *AsiairSwitch) GetSwitchDescription(id int) (string, error) { return s.slotAt(id).desc, nil }

func (s *AsiairSwitch) MinSwitchValue(id int) (float64, error) { return s.slotAt(id).min, nil }

func (s *AsiairSwitch) MaxSwitchValue(id int) (float64, error) { return s.slotAt(id).max, nil }

func (s *AsiairSwitch) SwitchStep(id int) (float64, error) { return s.slotAt(id).step, nil }

func (s *AsiairSwitch) GetSwitchValue(id int) (float64, error) {
	b, err := s.board()
	if err != nil {
		return 0, err
	}
	return s.slotAt(id).get(b)
}

// GetSwitch is the boolean view: false only at MinSwitchValue, per ISwitchV3.
func (s *AsiairSwitch) GetSwitch(id int) (bool, error) {
	v, err := s.GetSwitchValue(id)
	if err != nil {
		return false, err
	}
	return v != s.slotAt(id).min, nil
}

func (s *AsiairSwitch) SetSwitchValue(id int, value float64) error {
	sl := s.slotAt(id)
	if sl.set == nil {
		return alpacadev.ErrNotImplemented // read-only sensor
	}
	if value < sl.min || value > sl.max {
		return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "value outside MinSwitchValue..MaxSwitchValue")
	}
	b, err := s.board()
	if err != nil {
		return err
	}
	// Round an off-step value to the nearest step (ISwitchV3: not an error).
	value = sl.min + math.Round((value-sl.min)/sl.step)*sl.step
	return sl.set(b, value)
}

// SetSwitch is the boolean view: true drives MaxSwitchValue, false MinSwitchValue.
func (s *AsiairSwitch) SetSwitch(id int, state bool) error {
	sl := s.slotAt(id)
	if state {
		return s.SetSwitchValue(id, sl.max)
	}
	return s.SetSwitchValue(id, sl.min)
}

// SetSwitchName is not supported: the names describe fixed hardware functions.
func (s *AsiairSwitch) SetSwitchName(id int, name string) error {
	return alpacadev.ErrNotImplemented
}

// Every write here is a GPIO or sysfs-PWM poke that completes in microseconds, and
// every read is served from the library's telemetry cache, so nothing is
// asynchronous: CanAsync stays false (the BaseSwitch default) and the server gates
// the async members to NotImplemented. The one genuinely long-running operation on
// this board — a DSLR frame sequence — is on the Action seam, where it can report
// its own progress.
