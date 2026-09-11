package driver

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/am5"
)

// ASCOM Actions cover the AM5 features no standard Telescope member reaches. They follow the
// fleet convention: an EMPTY params reads the current value, a non-empty params sets it and
// echoes it back; read-only actions reject a value; operations take none. Names are advertised
// in CamelCase and matched case-insensitively.
//
// What is NOT here is as deliberate as what is. The mount mode is readable through the standard
// AlignmentMode property and the guide rate through GuideRate*, so this surface only SETS the
// mode — a client should not have to know a vendor action name to ask a question ASCOM already
// defines.
//
// Two settings the mount will not read back — the variable slew rate and the numbered rate index
// — are cached here and read back from that cache, which is the fleet's pattern for
// unreadable-from-hardware state (see mgpbox GpsEnable, tenmicron refraction).

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

// Action dispatches a custom action by name, matched case-insensitively against the CamelCase
// keys of actions().
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
func (t *Telescope) live() (*am5.Mount, error) {
	if m := t.mount(); m != nil {
		return m, nil
	}
	return nil, alpacadev.ErrNotConnected
}

// read wraps a mount read as a read-only action (rejects a params value).
func (t *Telescope) read(fn func(*am5.Mount) (string, error)) actionFn {
	return func(params string) (string, error) {
		if params != "" {
			return "", badValue("action is read-only (pass no value)")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		return fn(m)
	}
}

// readWrite wraps a read + write: empty params reads; a value sets it and is echoed.
func (t *Telescope) readWrite(get func(*am5.Mount) (string, error), set func(*am5.Mount, string) error) actionFn {
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

// op wraps a value-less operation; it returns the status string the operation reports.
func (t *Telescope) op(status string, fn func(*am5.Mount) error) actionFn {
	return func(params string) (string, error) {
		if params != "" {
			return "", badValue("action takes no value")
		}
		m, err := t.live()
		if err != nil {
			return "", err
		}
		if err := fn(m); err != nil {
			return "", err
		}
		return status, nil
	}
}

func badValue(msg string) error { return alpacadev.NewError(alpacadev.ErrNumInvalidValue, msg) }

// parseBoolArg accepts the spellings a hand-typed action call uses.
func parseBoolArg(p string) (bool, error) {
	switch strings.ToLower(p) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	}
	return false, badValue("expected true or false")
}

func (t *Telescope) actions() map[string]actionFn {
	return map[string]actionFn{
		// Mount mode — WRITE ONLY here, because reading it is AlignmentMode's job. Takes effect
		// at once on firmware 1.8.8 (see lx200/docs/am5-command-set.md); the mount acknowledges
		// nothing, so a caller that needs certainty reads AlignmentMode back.
		"MountMode": func(params string) (string, error) {
			// The argument is checked BEFORE the connection, as the read/op helpers do: a caller
			// who passed the wrong thing needs telling so whether or not a mount is attached, and
			// "not connected" would send them looking in the wrong place.
			var mode am5.MountMode
			switch strings.ToLower(params) {
			case "eq", "equatorial", "german", "germanpolar":
				mode = am5.ModeEquatorial
			case "altaz", "alt-az", "azimuthal":
				mode = am5.ModeAltAz
			case "":
				return "", badValue("pass equatorial or altaz; read the current mode with the standard AlignmentMode property")
			default:
				return "", badValue("expected equatorial or altaz")
			}
			m, err := t.live()
			if err != nil {
				return "", err
			}
			if err := m.SetMountMode(mode); err != nil {
				return "", err
			}
			if mode == am5.ModeAltAz {
				return "altaz", nil
			}
			return "equatorial", nil
		},

		// Identity. The USB descriptor serial is a factory default on the units seen so far, so
		// this is what tells two AM5s apart.
		"MAC": t.read(func(m *am5.Mount) (string, error) { return m.MAC() }),

		// Settings the mount reports.
		"Buzzer": t.readWrite(
			func(m *am5.Mount) (string, error) { v, err := m.Buzzer(); return strconv.Itoa(v), err },
			func(m *am5.Mount, p string) error {
				v, err := strconv.Atoi(p)
				if err != nil || v < 0 || v > 2 {
					return badValue("expected 0 (off), 1 (low) or 2 (high)")
				}
				return m.SetBuzzer(v)
			}),

		"HeavyDuty": t.readWrite(
			func(m *am5.Mount) (string, error) { v, err := m.HeavyDuty(); return strconv.FormatBool(v), err },
			func(m *am5.Mount, p string) error {
				on, err := parseBoolArg(p)
				if err != nil {
					return err
				}
				return m.SetHeavyDuty(on)
			}),

		// JSON both ways, as tenmicron's Optics action does: three fields that are set together
		// and mean nothing apart.
		"MeridianFlip": t.readWrite(
			func(m *am5.Mount) (string, error) {
				f, err := m.MeridianFlip()
				if err != nil {
					return "", err
				}
				b, err := json.Marshal(f)
				return string(b), err
			},
			func(m *am5.Mount, p string) error {
				var f am5.MeridianFlip
				if err := json.Unmarshal([]byte(p), &f); err != nil {
					return badValue("expected JSON: {\"Enabled\":bool,\"TrackPast\":bool,\"LimitDeg\":int}")
				}
				return m.SetMeridianFlip(f)
			}),

		// The tracking rate INDEX as the mount reports it. Read-only and raw: what the values
		// mean is not established — every capture answers 0 — so this is not TrackingRate and
		// must not be mistaken for it.
		"TrackingRateIndex": t.read(func(m *am5.Mount) (string, error) {
			i, err := m.TrackingRateIndex()
			return strconv.Itoa(i), err
		}),

		// Rates the mount will not read back: the driver caches what it last sent.
		"VariableSlewRate": t.readWrite(
			func(*am5.Mount) (string, error) { return strconv.FormatFloat(t.getF(&t.varSlewRate), 'f', 2, 64), nil },
			func(m *am5.Mount, p string) error {
				v, err := strconv.ParseFloat(p, 64)
				if err != nil || v < 0 || v > 1440 {
					return badValue("expected 0–1440 (× sidereal)")
				}
				if err := m.SetVariableSlewRate(v); err != nil {
					return err
				}
				t.setF(&t.varSlewRate, v)
				return nil
			}),

		"SlewRateIndex": t.readWrite(
			func(*am5.Mount) (string, error) { return strconv.Itoa(int(t.getF(&t.slewRateIdx))), nil },
			func(m *am5.Mount, p string) error {
				v, err := strconv.Atoi(p)
				if err != nil || v < 0 || v > 9 {
					return badValue("expected 0–9")
				}
				if err := m.SetSlewRateIndex(v); err != nil {
					return err
				}
				t.setF(&t.slewRateIdx, float64(v))
				return nil
			}),

		// Operations.
		"SetHome": t.op("home stored", func(m *am5.Mount) error { return m.SetHome() }),
		"ClearAlignment": t.op("alignment cleared", func(m *am5.Mount) error {
			return m.ClearAlignment()
		}),
	}
}
