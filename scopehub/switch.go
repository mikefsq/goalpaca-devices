package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

var _ alpacadev.Switch = (*Switch)(nil)

const ports = 6

// slot is one entry in the switch array.
type slot struct {
	name           func(l *scopehub.Labels) string
	desc           string
	min, max, step float64
	get            func(n *snapshot) (float64, error)
	set            func(d device, v float64) error
	rename         func(d device, name string) error
}

func b2f(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// notValid reports a reading the firmware marks invalid, for example a current
// monitor between short PWM pulses.
func notValid(what string) error {
	return alpacadev.NewError(alpacadev.ErrNumValueNotSet, what+" has no valid reading")
}

// portName gives the fixed port, a suffix such as "Duty", and the user's label
// in parentheses when the port has one: "DC 1 Duty (Camera)".
func portName(port, suffix, label string) string {
	n := port
	if suffix != "" {
		n += " " + suffix
	}
	if label != "" {
		n += " (" + label + ")"
	}
	return n
}

func dcName(l *scopehub.Labels, i int, suffix string) string {
	label := ""
	if l != nil {
		label = l.DC[i]
	}
	return portName(fmt.Sprintf("DC %d", i+1), suffix, label)
}

func usbName(l *scopehub.Labels, i int, suffix string) string {
	label := ""
	if l != nil {
		label = l.USB[i]
	}
	return portName(fmt.Sprintf("USB %d", i+1), suffix, label)
}

// labelFrom takes the label out of a name a client sets. A client may send
// the bare label or the full name it read back, "DC 1 (Camera)"; the fixed
// port alone, "DC 1", clears the label.
func labelFrom(fixedName, name string) string {
	name = strings.TrimSpace(name)
	if name == fixedName {
		return ""
	}
	if strings.HasPrefix(name, fixedName+" (") && strings.HasSuffix(name, ")") {
		return strings.TrimSpace(name[len(fixedName)+2 : len(name)-1])
	}
	return name
}

func fixed(s string) func(*scopehub.Labels) string { return func(*scopehub.Labels) string { return s } }

// buildSlots returns the switch array: the input supply first, then the port
// controls, then per-port readings, controller settings and environmental
// readings. The order is public once clients save profiles against it.
func buildSlots() []slot {
	var out []slot

	// 0..1: input supply, first so clients show it at the top.
	out = append(out,
		slot{
			name: fixed("Input Voltage"), desc: "12 V input (V)",
			min: 0, max: 36, step: 0.01,
			get: func(n *snapshot) (float64, error) {
				if !n.st.Input.Valid {
					return 0, notValid("Input Voltage")
				}
				return n.st.Input.Volts, nil
			},
		},
		slot{
			name: fixed("Input Current"), desc: "12 V input (A)",
			min: -20, max: 20, step: 0.01,
			get: func(n *snapshot) (float64, error) {
				if !n.st.Input.Valid {
					return 0, notValid("Input Current")
				}
				return n.st.Input.Amps, nil
			},
		},
	)

	// 2..7: DC power. On runs the output at its duty setpoint.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return dcName(l, i, "") },
			desc: fmt.Sprintf("DC %d: 12 V output power", i+1),
			min:  0, max: 1, step: 1,
			get:    func(n *snapshot) (float64, error) { return b2f(n.st.DC[i].Enabled), nil },
			set:    func(d device, v float64) error { return d.SetDCEnabled(i+1, v >= 0.5) },
			rename: func(d device, name string) error { return d.SetDCLabel(i+1, name) },
		})
	}

	// 8..13: USB power. Off forces VBUS off.
	for i := 0; i < ports; i++ {
		i := i
		desc := fmt.Sprintf("USB %d: USB-C port power", i+1)
		if i == 5 {
			desc += ". Raspberry Pi power port"
		}
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return usbName(l, i, "") },
			desc: desc,
			min:  0, max: 1, step: 1,
			get:    func(n *snapshot) (float64, error) { return b2f(n.st.USB[i].Enabled), nil },
			set:    func(d device, v float64) error { return d.SetUSB(i+1, v >= 0.5) },
			rename: func(d device, name string) error { return d.SetUSBLabel(i+1, name) },
		})
	}

	// 14..19: DC duty setpoints, kept while the output is off.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return dcName(l, i, "Duty") },
			desc: fmt.Sprintf("DC %d: pwm duty cycle (%%).", i+1),
			min:  0, max: 100, step: 1,
			get: func(n *snapshot) (float64, error) { return float64(n.st.DC[i].Duty), nil },
			set: func(d device, v float64) error { return d.SetDC(i+1, int(v)) },
		})
	}

	// 20..25: dew auto control. On, the board sets the duty from the dew margin.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return dcName(l, i, "Dew Auto") },
			desc: fmt.Sprintf("DC %d: duty follows the dew margin", i+1),
			min:  0, max: 1, step: 1,
			get: func(n *snapshot) (float64, error) { return b2f(n.dew.Auto[i]), nil },
			set: func(d device, v float64) error { return d.SetDCAuto(i+1, v >= 0.5) },
		})
	}

	// 26: LED brightness.
	out = append(out, slot{
		name: fixed("LED Brightness"), desc: "Brightness of all indicator LEDs (%)",
		min: 0, max: 100, step: 1,
		get: func(n *snapshot) (float64, error) { return float64(n.st.LEDBrightness), nil },
		set: func(d device, v float64) error { return d.SetLEDBrightness(int(v)) },
	})

	// 27..32: DC current, averaged over a PWM period.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return dcName(l, i, "Current") },
			desc: fmt.Sprintf("DC %d: current (A)", i+1),
			min:  -20, max: 20, step: 0.001,
			get: func(n *snapshot) (float64, error) {
				if !n.st.DC[i].AmpsValid {
					return 0, notValid(fmt.Sprintf("DC %d current", i+1))
				}
				return n.st.DC[i].Amps, nil
			},
		})
	}

	// 33..38: USB current.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return usbName(l, i, "Current") },
			desc: fmt.Sprintf("USB %d: VBUS current (A)", i+1),
			min:  -10, max: 10, step: 0.001,
			get: func(n *snapshot) (float64, error) {
				if !n.st.USB[i].Valid {
					return 0, notValid(fmt.Sprintf("USB %d current", i+1))
				}
				return n.st.USB[i].Amps, nil
			},
		})
	}

	// 39..44: USB attach state.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return usbName(l, i, "Attached") },
			desc: fmt.Sprintf("USB %d: a device is attached", i+1),
			min:  0, max: 1, step: 1,
			get: func(n *snapshot) (float64, error) { return b2f(n.st.USB[i].Attached), nil },
		})
	}

	// 45..49: controller settings, written as a whole after changing one field.
	setting := func(name, desc string, min, max, step float64, field func(*scopehub.DewSettings) *float64) slot {
		return slot{
			name: fixed(name), desc: desc, min: min, max: max, step: step,
			get: func(n *snapshot) (float64, error) { return math.Round(*field(&n.dew.DewSettings)/step) * step, nil },
			set: func(d device, v float64) error {
				w, err := d.Dew()
				if err != nil {
					return err
				}
				s := w.DewSettings
				*field(&s) = v
				return d.SetDewSettings(s)
			},
		}
	}
	out = append(out,
		setting("Temperature Offset", "Added to the board sensor temperature to get outside air (C)", -20, 20, 0.1,
			func(s *scopehub.DewSettings) *float64 { return &s.TempOffsetC }),
		setting("Dew Start Margin", "Air minus dew point where heating starts (C)", 0, 30, 0.1,
			func(s *scopehub.DewSettings) *float64 { return &s.StartC }),
		setting("Dew Full Margin", "Air minus dew point where heating reaches the maximum (C)", -10, 30, 0.1,
			func(s *scopehub.DewSettings) *float64 { return &s.FullC }),
		setting("Dew Min Duty", "Heater duty at the start margin (%)", 0, 100, 1,
			func(s *scopehub.DewSettings) *float64 { return &s.MinPct }),
		setting("Dew Max Duty", "Heater duty cap (%)", 0, 100, 1,
			func(s *scopehub.DewSettings) *float64 { return &s.MaxPct }),
	)

	// 50..51: PWM frequency and LED auto.
	out = append(out,
		slot{
			name: fixed("DC PWM Frequency"), desc: "PWM frequency shared by the DC outputs (Hz)",
			min: 0.1, max: 10, step: 0.1,
			get: func(n *snapshot) (float64, error) { return math.Round(n.st.DCPWMHz*10) / 10, nil },
			set: func(d device, v float64) error { _, err := d.SetDCPWMHz(v); return err },
		},
		slot{
			name: fixed("LED Auto"), desc: "Indicator LED brightness follows the light sensor",
			min: 0, max: 1, step: 1,
			get: func(n *snapshot) (float64, error) { return b2f(n.aux.LEDAuto), nil },
			set: func(d device, v float64) error { return d.SetLEDAuto(v >= 0.5) },
		},
	)

	// 52..55: dew and light readings.
	dewReading := func(name, desc string, min, max float64, v func(*scopehub.Dew) float64) slot {
		return slot{
			name: fixed(name), desc: desc, min: min, max: max, step: 0.01,
			get: func(n *snapshot) (float64, error) {
				if !n.dew.Valid {
					return 0, notValid(name)
				}
				return v(n.dew), nil
			},
		}
	}
	out = append(out,
		dewReading("Air Temperature", "Outside air: board sensor plus offset (C)", -60, 80,
			func(w *scopehub.Dew) float64 { return w.AmbientC }),
		dewReading("Dew Point", "Dew point from the board sensor (C)", -80, 60,
			func(w *scopehub.Dew) float64 { return w.DewpointC }),
		dewReading("Dew Margin", "Air temperature minus dew point (C)", -60, 120,
			func(w *scopehub.Dew) float64 { return w.MarginC }),
		slot{
			name: fixed("Light Level"), desc: "Ambient light at the board (lux)",
			min: 0, max: 150000, step: 0.0001,
			get: func(n *snapshot) (float64, error) {
				if !n.st.Env.LuxValid {
					return 0, notValid("Light Level")
				}
				return n.st.Env.Lux, nil
			},
		},
	)
	// 56..61: PWM per DC output. Off holds a switched-on output steady at
	// 100%; on runs it at its duty setpoint. Off also ends dew auto.
	for i := 0; i < ports; i++ {
		i := i
		out = append(out, slot{
			name: func(l *scopehub.Labels) string { return dcName(l, i, "PWM") },
			desc: fmt.Sprintf("DC %d: run at the duty setpoint; off holds 100%%", i+1),
			min:  0, max: 1, step: 1,
			get: func(n *snapshot) (float64, error) { return b2f(n.st.DC[i].PWM), nil },
			set: func(d device, v float64) error { return d.SetDCPWM(i+1, v >= 0.5) },
		})
	}
	// 62..63: barometer readings. Append to preserve every existing switch ID.
	for _, reading := range []struct {
		name, description string
		min, max          float64
		value             func(scopehub.Env) float64
	}{
		{"Pressure", "Station pressure at the board (hPa)", 0, 2000, func(e scopehub.Env) float64 { return e.PressureHPa }},
		{"Barometer Temperature", "Raw barometer sensor temperature (C)", -60, 125, func(e scopehub.Env) float64 { return e.BaroTempC }},
	} {
		out = append(out, slot{
			name: fixed(reading.name), desc: reading.description,
			min: reading.min, max: reading.max, step: 0.01,
			get: func(n *snapshot) (float64, error) {
				if !n.st.Env.BaroValid {
					return 0, notValid(reading.name)
				}
				return reading.value(n.st.Env), nil
			},
		})
	}
	return out
}

// Switch is the ASCOM Switch device for one scope_hub board.
type Switch struct {
	alpacadev.BaseSwitch
	b     *board
	slots []slot
}

// NewSwitch creates the device. It does not touch the hardware.
func NewSwitch(cfg Config) *Switch {
	s := &Switch{b: newBoard(cfg.Serial), slots: buildSlots()}
	s.Version = "0.2.0"
	s.Info = "scopehub: scope_hub USB hub controller over USB HID"
	s.IfaceVer = alpacadev.InterfaceVersionSwitch
	s.ID = "ScopeHub-switch"
	if cfg.Serial != "" {
		s.ID += "-" + cfg.Serial
	}
	s.DevName = "Scope Hub"
	s.Desc = "scope_hub: 12 V outputs with PWM, USB-C port power, current monitoring"
	return s
}

func (s *Switch) Open(ctx context.Context) error  { return s.b.Open(ctx) }
func (s *Switch) Close(ctx context.Context) error { s.MarkDisconnected(); return s.b.Close(ctx) }

// Connect requires a reachable board.
func (s *Switch) Connect(ctx context.Context) error {
	if _, err := s.b.Info(); err != nil {
		return err
	}
	s.MarkConnected()
	return nil
}

// Disconnect ends the client session and leaves every output as it is. Power
// to cameras, mounts and heaters must not depend on a client staying connected.
func (s *Switch) Disconnect(ctx context.Context) error {
	s.MarkDisconnected()
	return nil
}

func (s *Switch) snapshot() (*snapshot, error) {
	if !s.Connected() {
		return nil, alpacadev.ErrNotConnected
	}
	return s.b.Snapshot()
}

func (s *Switch) MaxSwitch() int { return len(s.slots) }

// at is unchecked: the server range-checks the ID against MaxSwitch first.
func (s *Switch) at(id int) *slot { return &s.slots[id] }

func (s *Switch) CanWrite(id int) (bool, error)               { return s.at(id).set != nil, nil }
func (s *Switch) GetSwitchDescription(id int) (string, error) { return s.at(id).desc, nil }
func (s *Switch) MinSwitchValue(id int) (float64, error)      { return s.at(id).min, nil }
func (s *Switch) MaxSwitchValue(id int) (float64, error)      { return s.at(id).max, nil }
func (s *Switch) SwitchStep(id int) (float64, error)          { return s.at(id).step, nil }

// GetSwitchName returns the port's label from the board, or its fixed name.
// Without a connection, fixed names stand in for labels.
func (s *Switch) GetSwitchName(id int) (string, error) {
	var l *scopehub.Labels
	if s.Connected() {
		if n, err := s.b.Snapshot(); err == nil {
			l = n.labels
		}
	}
	return s.at(id).name(l), nil
}

func (s *Switch) GetSwitchValue(id int) (float64, error) {
	n, err := s.snapshot()
	if err != nil {
		return 0, err
	}
	return s.at(id).get(n)
}

// GetSwitch is the boolean view: false only at MinSwitchValue.
func (s *Switch) GetSwitch(id int) (bool, error) {
	v, err := s.GetSwitchValue(id)
	if err != nil {
		return false, err
	}
	return v != s.at(id).min, nil
}

func (s *Switch) SetSwitchValue(id int, value float64) error {
	sl := s.at(id)
	if sl.set == nil {
		return alpacadev.ErrNotImplemented
	}
	if value < sl.min || value > sl.max {
		return alpacadev.NewError(alpacadev.ErrNumInvalidValue, "value outside MinSwitchValue..MaxSwitchValue")
	}
	if !s.Connected() {
		return alpacadev.ErrNotConnected
	}
	// ISwitchV3 rounds an off-step value to the nearest step.
	value = sl.min + math.Round((value-sl.min)/sl.step)*sl.step
	value = math.Round(value*1e6) / 1e6 // drop float drift from fractional steps
	return valueErr(s.b.Do(func(d device) error { return sl.set(d, value) }))
}

// SetSwitch drives MaxSwitchValue for true and MinSwitchValue for false.
func (s *Switch) SetSwitch(id int, state bool) error {
	sl := s.at(id)
	if state {
		return s.SetSwitchValue(id, sl.max)
	}
	return s.SetSwitchValue(id, sl.min)
}

// SetSwitchName sets a port power switch's label, stored on the board in
// flash. Names always lead with the fixed port, "DC 1 (Camera)", and the
// port's duty, current and dew switches carry the same label. An empty name or
// the fixed port alone clears it. Other switches keep fixed names.
func (s *Switch) SetSwitchName(id int, name string) error {
	sl := s.at(id)
	if sl.rename == nil {
		return alpacadev.ErrNotImplemented
	}
	if !s.Connected() {
		return alpacadev.ErrNotConnected
	}
	name = labelFrom(sl.name(nil), name)
	return valueErr(s.b.Do(func(d device) error { return sl.rename(d, name) }))
}

// valueErr turns a range rejection into an InvalidValue error.
func valueErr(err error) error {
	if errors.Is(err, scopehub.ErrOutOfRange) {
		return alpacadev.NewError(alpacadev.ErrNumInvalidValue, err.Error())
	}
	return err
}

// Every command completes within one USB control transfer, so CanAsync stays
// false and the server rejects the async members.
