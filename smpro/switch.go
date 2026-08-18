package driver

import (
	"context"
	"math"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

var _ alpacadev.Switch = (*SMProSwitch)(nil)

// slot is one entry in the switch array: its ASCOM metadata and the Board
// accessors behind it. Boolean slots use min 0 / max 1 / step 1 (the ISwitchV3
// binary convention, Max−Min == Step); set is nil for read-only sensors.
type slot struct {
	name, desc     string
	min, max, step float64
	get            func(b *stellarmate.Board) (float64, error)
	set            func(b *stellarmate.Board, v float64) error
}

// b2f renders a boolean reading as the 0/1 analog view of a binary switch.
func b2f(v bool, err error) (float64, error) {
	if v {
		return 1, err
	}
	return 0, err
}

// slots is the SM Pro switch array. Order is part of the public surface once
// clients save profiles against it; append rather than reorder.
var slots = []slot{
	// 0..3 — the four switched power outputs.
	{name: "Power 1", desc: "12 V power output 1", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.Power(0)) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetPower(0, v >= 0.5) }},
	{name: "Power 2", desc: "12 V power output 2", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.Power(1)) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetPower(1, v >= 0.5) }},
	{name: "Power 3", desc: "12 V power output 3", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.Power(2)) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetPower(2, v >= 0.5) }},
	{name: "Power 4", desc: "12 V power output 4", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.Power(3)) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetPower(3, v >= 0.5) }},

	// 4..5 — dew heaters, duty-cycle percent. 0 % is a genuine off (the PWM
	// channel is disabled), so the boolean view needs no separate on/off slot.
	{name: "Dew Heater 1", desc: "Dew heater 1 duty cycle (%)", min: 0, max: 100, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { d, err := b.DewDuty(0); return float64(d), err },
		set: func(b *stellarmate.Board, v float64) error { return b.SetDewDuty(0, int(math.Round(v))) }},
	{name: "Dew Heater 2", desc: "Dew heater 2 duty cycle (%)", min: 0, max: 100, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { d, err := b.DewDuty(1); return float64(d), err },
		set: func(b *stellarmate.Board, v float64) error { return b.SetDewDuty(1, int(math.Round(v))) }},

	// 6..7 — the variable DC output: an enable toggle plus the voltage level.
	//
	// The maximum is 11 V, not 12: the DAC's ceiling is ~0.8565*Vin, measured at
	// 11.56 V on a 13.5 V supply, so a commanded 12 V clamps and reads back ~11.5.
	// ASCOM requires a static MaxSwitchValue, and one a client can actually reach —
	// GetSwitchValue must return what SetSwitchValue set. (The ceiling falls with
	// the supply: ~10.3 V from a 12 V battery, which 11 V would then overshoot. Drop
	// this to 10 if the rig ever runs off a battery. Board.VariableOutputMax()
	// reports the live ceiling.)
	//
	// This slot is the *programmed setpoint*, deliberately not the measured rail:
	// the output has a bulk capacitor that decays over tens of seconds after the
	// enable is dropped (11 V -> 0.8 V in ~10 s unloaded), so a measurement-backed
	// value would drift below MinSwitchValue and never settle. The measurement is
	// slot 16, read-only.
	{name: "Variable Output", desc: "Variable DC output enable", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.VariableEnable()) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetVariableEnable(v >= 0.5) }},
	{name: "Variable Voltage", desc: "Variable DC output setpoint (V)", min: 3, max: 11, step: 0.1,
		get: func(b *stellarmate.Board) (float64, error) { return b.VariableVoltage() },
		set: func(b *stellarmate.Board, v float64) error { return b.SetVariableVoltage(v) }},

	// 8..10 — LEDs and the GP2 antenna rail.
	{name: "Indicator LEDs", desc: "Connector indicator-LED bank enable", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.IndicatorLEDs()) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetIndicatorLEDs(v >= 0.5) }},
	{name: "User LED", desc: "USER status LED", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.UserLED()) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetUserLED(v >= 0.5) }},
	{name: "Antenna Power", desc: "GPS antenna power enable (wired on later boards)", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) { return b2f(b.AntennaPower()) },
		set: func(b *stellarmate.Board, v float64) error { return b.SetAntennaPower(v >= 0.5) }},

	// 11..12 — auto-dew enables. Ramp parameters and the weather feed are on
	// the Action interface (see actions.go).
	{name: "Auto Dew 1", desc: "Auto-dew control for dew heater 1", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) {
			cfg, err := b.AutoDew(0)
			return b2f(cfg.Enabled, err)
		},
		set: func(b *stellarmate.Board, v float64) error { return setAutoDewEnabled(b, 0, v >= 0.5) }},
	{name: "Auto Dew 2", desc: "Auto-dew control for dew heater 2", min: 0, max: 1, step: 1,
		get: func(b *stellarmate.Board) (float64, error) {
			cfg, err := b.AutoDew(1)
			return b2f(cfg.Enabled, err)
		},
		set: func(b *stellarmate.Board, v float64) error { return setAutoDewEnabled(b, 1, v >= 0.5) }},

	// 13..15 — read-only sensors (set == nil → CanWrite false). The current
	// senses are raw 12-bit ADC codes: the amps scaling of those channels has
	// not been characterised, and an honest raw beats a made-up unit.
	{name: "Input Voltage", desc: "Input supply voltage (V, read-only)", min: 0, max: 30, step: 0.01,
		get: func(b *stellarmate.Board) (float64, error) { return b.Voltage() }},
	{name: "Power 1 Current", desc: "Power output 1 current sense (raw ADC code, read-only)", min: 0, max: 4095, step: 1,
		get: func(b *stellarmate.Board) (float64, error) {
			c, err := b.PowerChannel1CurrentRaw()
			return float64(c), err
		}},
	{name: "Dew 1 Current", desc: "Dew heater 1 current sense (raw ADC code, read-only)", min: 0, max: 4095, step: 1,
		get: func(b *stellarmate.Board) (float64, error) {
			c, err := b.DewChannel1CurrentRaw()
			return float64(c), err
		}},

	// 16 — what is actually on the variable-output connector (ADC ch4), as opposed
	// to the setpoint at slot 7. This is where a client sees the rail sag under
	// load, sees a clamped setpoint for what it is, and sees the output decay to
	// zero after the enable is dropped. Read-only, and its range starts at 0
	// because a disabled output really is 0 V.
	{name: "Variable Output Measured", desc: "Variable DC output, measured at the connector (V, read-only)",
		min: 0, max: 12, step: 0.01,
		get: func(b *stellarmate.Board) (float64, error) { return b.VariableOutputVolts() }},
}

func setAutoDewEnabled(b *stellarmate.Board, ch int, on bool) error {
	cfg, err := b.AutoDew(ch)
	if err != nil {
		return err
	}
	cfg.Enabled = on
	return b.SetAutoDew(ch, cfg)
}

// SMProSwitch is the ASCOM Switch device over the SM Pro board.
type SMProSwitch struct {
	alpacadev.BaseSwitch
	hub *Hub
	session
}

// NewSwitch creates the Switch device on a shared Hub.
func NewSwitch(hub *Hub) *SMProSwitch {
	s := &SMProSwitch{hub: hub}
	s.Version = "0.1.0"
	s.Info = "smpro — StellarMate SM Pro power/dew/variable-output Alpaca driver over Go stellarmate"
	s.IfaceVer = alpacadev.InterfaceVersionSwitch
	s.ID = "SMPro-switch"
	s.DevName = "SM Pro Power"
	s.Desc = "StellarMate SM Pro controller: power outputs, dew heaters, variable output, sensors"
	return s
}

// --- Hardware lifecycle (persistent owner, shared with the Focuser) ---

func (s *SMProSwitch) Open(ctx context.Context) error { return s.hub.Open(ctx) }

func (s *SMProSwitch) Close(ctx context.Context) error {
	s.setConnected(false)
	return s.hub.Close(ctx)
}

func (s *SMProSwitch) Connect(ctx context.Context) error {
	if !s.hub.Ready() {
		return alpacadev.ErrNotConnected
	}
	s.setConnected(true)
	return nil
}

func (s *SMProSwitch) Connected() bool { return s.isConnected() }

// Disconnect ends this client's ASCOM session. It deliberately does NOT release
// the board: the power outputs, the dew heaters and the variable rail must keep
// running when a client goes away. The hardware is the process's, not the client's
// — see session.
func (s *SMProSwitch) Disconnect(ctx context.Context) error {
	s.setConnected(false)
	return nil
}

// board gates every operational member on the session being live.
func (s *SMProSwitch) board() (*stellarmate.Board, error) { return boardFor(&s.session, s.hub) }

// --- ISwitchV3 ---

func (s *SMProSwitch) MaxSwitch() int { return len(slots) }

// slotAt is safe unchecked indexing: the server range-checks the Id against
// MaxSwitch before every call.
func slotAt(id int) *slot { return &slots[id] }

func (s *SMProSwitch) CanWrite(id int) (bool, error) { return slotAt(id).set != nil, nil }

func (s *SMProSwitch) GetSwitchName(id int) (string, error) { return slotAt(id).name, nil }

func (s *SMProSwitch) GetSwitchDescription(id int) (string, error) { return slotAt(id).desc, nil }

func (s *SMProSwitch) MinSwitchValue(id int) (float64, error) { return slotAt(id).min, nil }

func (s *SMProSwitch) MaxSwitchValue(id int) (float64, error) { return slotAt(id).max, nil }

func (s *SMProSwitch) SwitchStep(id int) (float64, error) { return slotAt(id).step, nil }

func (s *SMProSwitch) GetSwitchValue(id int) (float64, error) {
	b, err := s.board()
	if err != nil {
		return 0, err
	}
	return slotAt(id).get(b)
}

// GetSwitch is the boolean view: false only at MinSwitchValue, per ISwitchV3.
func (s *SMProSwitch) GetSwitch(id int) (bool, error) {
	v, err := s.GetSwitchValue(id)
	if err != nil {
		return false, err
	}
	return v != slotAt(id).min, nil
}

func (s *SMProSwitch) SetSwitchValue(id int, value float64) error {
	sl := slotAt(id)
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
func (s *SMProSwitch) SetSwitch(id int, state bool) error {
	sl := slotAt(id)
	if state {
		return s.SetSwitchValue(id, sl.max)
	}
	return s.SetSwitchValue(id, sl.min)
}

// SetSwitchName is not supported: the names describe fixed hardware functions.
func (s *SMProSwitch) SetSwitchName(id int, name string) error {
	return alpacadev.ErrNotImplemented
}

// Every SM Pro switch operation is an I2C/PWM write that completes in
// microseconds, so nothing here is asynchronous: CanAsync stays false (the
// BaseSwitch default) and the server gates the async members to NotImplemented.
