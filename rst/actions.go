package driver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/rst"
)

// ASCOM Actions are single (name, params) → string calls, not get/set pairs, so this
// package gives them a consistent shape over the RST's extras that no standard member
// covers: an EMPTY params reads the current value; a non-empty params sets it (and
// echoes it back). A few are read-only (telemetry), take an index (site/speed), or
// are operations (PolarAxis). Names are advertised in CamelCase; matching is
// case-insensitive (a driver choice, not an Alpaca requirement).

type actionFn func(params string) (string, error)

// SupportedActions lists the custom action names (sorted, for a stable response).
func (t *Telescope) SupportedActions() []string {
	reg := t.actions()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Action dispatches a custom action by name, matched case-insensitively against the
// CamelCase keys of actions().
func (t *Telescope) Action(name, params string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for n, fn := range t.actions() {
		if strings.ToLower(n) == want {
			return fn(strings.TrimSpace(params))
		}
	}
	return "", alpacadev.ErrActionNotImplemented
}

// live returns the connected mount or ErrNotConnected.
func (t *Telescope) live() (*rst.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// read wraps a mount read as a read-only action (rejects a params value).
func (t *Telescope) read(fn func(*rst.Mount) (string, error)) actionFn {
	return func(params string) (string, error) {
		if params != "" {
			return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "action is read-only (pass no value)")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		return fn(m)
	}
}

// readWrite wraps a mount read + write: empty params reads; a value sets it and is
// echoed back.
func (t *Telescope) readWrite(get func(*rst.Mount) (string, error), set func(*rst.Mount, string) error) actionFn {
	return func(params string) (string, error) {
		m, err := t.live()
		if err != nil {
			return "", err
		}
		if params == "" {
			return get(m)
		}
		if err := set(m, params); err != nil {
			return "", err
		}
		return params, nil
	}
}

// indexed wraps a read that takes a 1-based index (site names, slew speeds): the
// params value is the index (default 1).
func (t *Telescope) indexed(fn func(*rst.Mount, int) (string, error)) actionFn {
	return func(params string) (string, error) {
		m, err := t.live()
		if err != nil {
			return "", err
		}
		n := 1
		if params != "" {
			if v, err := strconv.Atoi(params); err == nil {
				n = v
			} else {
				return "", alpacadev.ErrInvalidValue
			}
		}
		return fn(m, n)
	}
}

func (t *Telescope) actions() map[string]actionFn {
	return map[string]actionFn{
		// operation: slew the OTA to the polar axis (async — poll Slewing/AtHome).
		"PolarAxis": func(params string) (string, error) {
			if params != "" {
				return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "PolarAxis takes no value")
			}
			m, err := t.live()
			if err != nil {
				return "", err
			}
			if err := m.SlewToPole(); err != nil {
				return "", err
			}
			return "slewing", nil
		},

		// read-only status / telemetry / clock
		"HomeFound": t.read(func(m *rst.Mount) (string, error) { return strconv.FormatBool(m.HomeFound()), nil }),
		"Fault": t.read(func(m *rst.Mount) (string, error) {
			f := m.Fault()
			if f == "" {
				f = "none"
			}
			return f, nil
		}),
		"Voltage":    t.read(func(m *rst.Mount) (string, error) { v, err := m.Voltage(); return f(v, 1), err }),
		"AutoResume": t.read(func(m *rst.Mount) (string, error) { v, err := m.AutoResume(); return strconv.FormatBool(v), err }),
		"LocalTime":  t.read(func(m *rst.Mount) (string, error) { v, err := m.LocalTime(); return f(v, 5), err }),
		"Date":       t.read(func(m *rst.Mount) (string, error) { return m.Date() }),
		"UTCOffset":  t.read(func(m *rst.Mount) (string, error) { v, err := m.UTCOffset(); return f(v, 1), err }),
		"MotorLoad": t.read(func(m *rst.Mount) (string, error) {
			d, r, err := m.MotorLoad()
			return fmt.Sprintf("dec=%.1f,ra=%.1f", d, r), err
		}),
		"SystemStatus": t.read(func(m *rst.Mount) (string, error) {
			s, err := m.SystemStatus()
			return fmt.Sprintf("tcs=%v,dec=%v,ra=%v", s.TCS, s.DecMotor, s.RAMotor), err
		}),

		// read/write config
		"GuideRate": t.readWrite(
			func(m *rst.Mount) (string, error) { v, err := m.GuideRate(); return f(v, 2), err },
			func(m *rst.Mount, p string) error {
				v, err := strconv.ParseFloat(p, 64)
				if err != nil {
					return alpacadev.ErrInvalidValue
				}
				return m.SetGuideRate(v)
			}),

		// set-only (the RST does not report the current force-flip state)
		"ForcePierFlip": func(params string) (string, error) {
			m, err := t.live()
			if err != nil {
				return "", err
			}
			if params == "" {
				return "", alpacadev.NewError(alpacadev.ErrNumInvalidValue, "pass on/off (the mount does not report the current setting)")
			}
			on := params == "on" || params == "1" || params == "true"
			if err := m.SetForcePierFlip(on); err != nil {
				return "", err
			}
			return strconv.FormatBool(on), nil
		},

		// indexed reads
		"SiteName":  t.indexed(func(m *rst.Mount, n int) (string, error) { return m.SiteName(n) }),
		"SlewSpeed": t.indexed(func(m *rst.Mount, n int) (string, error) { v, err := m.SlewSpeed(n); return strconv.Itoa(v), err }),

		// instrument profile (see optics.go): empty params reads the current optics,
		// a JSON payload patches it. Both names are dual-mode, matching tenmicron so a
		// client drives optics identically regardless of which mount is configured.
		"setoptics": t.actionSetOptics,
		"optics":    t.actionSetOptics,
	}
}

// f formats a float with prec decimals (used to keep the action bodies compact).
func f(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
