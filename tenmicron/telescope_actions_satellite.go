package driver

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mikefsq/lx200/tenmicron"
)

// Satellite / TLE / trajectory actions: two-line-element loading, satellite ephemeris,
// transit precalculation and slewing, manual az/alt trajectories, and trajectory-follow
// offsets. Merged into the action registry by actions().

func (t *Telescope) satelliteActions() map[string]actionFn {
	return map[string]actionFn{
		// TLE loading (RW: read the loaded element set, write a new one)
		"loadtle": t.readWrite(
			func(m *tenmicron.Mount) (string, error) { return m.LoadedTLE() },
			func(m *tenmicron.Mount, p string) error { return m.LoadTLE(p) }),
		"databasetlecount": t.read(func(m *tenmicron.Mount) (string, error) { n, err := m.DatabaseTLECount(); return strconv.Itoa(n), err }),
		"loaddatabasetle":  t.indexed(func(m *tenmicron.Mount, n int) (string, error) { return m.LoadDatabaseTLE(n) }),

		// satellite ephemeris (params: Julian date, or empty/"now" for the mount clock)
		"satelliteequatorial": func(p string) (string, error) {
			m, err := t.live()
			if err != nil {
				return "", err
			}
			jd, err := t.resolveJD(m, p)
			if err != nil {
				return "", err
			}
			ra, dec, err := m.SatelliteEquatorial(jd)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]float64{"ra": ra, "dec": dec, "jd": jd})
			return string(b), nil
		},
		"satellitehorizontal": func(p string) (string, error) {
			m, err := t.live()
			if err != nil {
				return "", err
			}
			jd, err := t.resolveJD(m, p)
			if err != nil {
				return "", err
			}
			alt, az, err := m.SatelliteHorizontal(jd)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]float64{"alt": alt, "az": az, "jd": jd})
			return string(b), nil
		},

		// transit precalc + slew
		"precalctransit": t.actionPrecalcTransit,
		"slewtotransit":  t.op(func(m *tenmicron.Mount) (string, error) { return "slewing to transit", m.SlewToTransit() }),
		"transitstatus":  t.read(func(m *tenmicron.Mount) (string, error) { s, err := m.TransitSlewStatus(); return transitStr(s), err }),

		// manual az/alt trajectory build + replay
		"newtrajectory": func(p string) (string, error) {
			m, err := t.live()
			if err != nil {
				return "", err
			}
			jd, err := t.resolveJD(m, p)
			if err != nil {
				return "", err
			}
			if err := m.NewTrajectory(jd); err != nil {
				return "", err
			}
			return "trajectory started", nil
		},
		"addtrajectorypoint": func(p string) (string, error) {
			var pt struct {
				Az  float64 `json:"az"`
				Alt float64 `json:"alt"`
			}
			if err := json.Unmarshal([]byte(p), &pt); err != nil {
				return "", badValue("addtrajectorypoint: want JSON {az, alt}")
			}
			m, err := t.live()
			if err != nil {
				return "", err
			}
			n, err := m.AddTrajectoryPoint(pt.Az, pt.Alt)
			if err != nil {
				return "", err
			}
			return strconv.Itoa(n), nil
		},
		"precalctrajectory": t.op(func(m *tenmicron.Mount) (string, error) {
			tr, err := m.PrecalcTrajectory()
			if err != nil {
				return "", err
			}
			return transitJSON(tr), nil
		}),
		"replaytrajectory": t.op(func(m *tenmicron.Mount) (string, error) {
			tr, err := m.ReplayTrajectory()
			if err != nil {
				return "", err
			}
			return transitJSON(tr), nil
		}),

		// trajectory-follow offsets (id: axis1|axis2|axis1sky|time)
		"trajectoryoffset":       t.actionTrajectoryOffset,
		"addtrajectoryoffset":    t.actionAddTrajectoryOffset,
		"cleartrajectoryoffsets": t.op(func(m *tenmicron.Mount) (string, error) { return "cleared", m.ClearTrajectoryOffsets() }),
	}
}

// resolveJD reads the mount's current Julian date when params is empty or "now", else
// parses params as a Julian date.
func (t *Telescope) resolveJD(m *tenmicron.Mount, p string) (float64, error) {
	if p == "" || strings.EqualFold(p, "now") {
		return m.JulianDate()
	}
	return parseFloatArg(p)
}

// actionPrecalcTransit precalculates the next satellite transit. Payload is optional
// JSON {"jd":…, "minutes":…}; jd defaults to the mount clock and minutes to 60.
func (t *Telescope) actionPrecalcTransit(params string) (string, error) {
	m, err := t.live()
	if err != nil {
		return "", err
	}
	req := struct {
		JD      float64 `json:"jd"`
		Minutes int     `json:"minutes"`
	}{}
	if params != "" {
		if err := json.Unmarshal([]byte(params), &req); err != nil {
			return "", badValue("precalctransit: invalid JSON: " + err.Error())
		}
	}
	if req.JD == 0 {
		if jd, err := m.JulianDate(); err == nil {
			req.JD = jd
		}
	}
	if req.Minutes == 0 {
		req.Minutes = 60
	}
	tr, err := m.PrecalcTransit(req.JD, req.Minutes)
	if err != nil {
		return "", err
	}
	return transitJSON(tr), nil
}

// actionTrajectoryOffset reads (id) or sets (id=value) a trajectory-follow offset.
func (t *Telescope) actionTrajectoryOffset(params string) (string, error) {
	m, err := t.live()
	if err != nil {
		return "", err
	}
	name, valStr, hasVal := strings.Cut(params, "=")
	id, ok := parseTrajOffset(name)
	if !ok {
		return "", badValue("trajectoryoffset: want axis1|axis2|axis1sky|time (optionally =value)")
	}
	if !hasVal {
		v, err := m.TrajectoryOffsetValue(id)
		if err != nil {
			return "", err
		}
		return ftoa(v, 3), nil
	}
	v, err := parseFloatArg(valStr)
	if err != nil {
		return "", err
	}
	if err := m.SetTrajectoryOffset(id, v); err != nil {
		return "", err
	}
	return valStr, nil
}

// actionAddTrajectoryOffset adds to a trajectory-follow offset (id=value).
func (t *Telescope) actionAddTrajectoryOffset(params string) (string, error) {
	name, valStr, hasVal := strings.Cut(params, "=")
	if !hasVal {
		return "", badValue("addtrajectoryoffset: want axis=value")
	}
	id, ok := parseTrajOffset(name)
	if !ok {
		return "", badValue("addtrajectoryoffset: want axis1|axis2|axis1sky|time")
	}
	v, err := parseFloatArg(valStr)
	if err != nil {
		return "", err
	}
	m, err := t.live()
	if err != nil {
		return "", err
	}
	if err := m.AddTrajectoryOffset(id, v); err != nil {
		return "", err
	}
	return valStr, nil
}

func parseTrajOffset(s string) (tenmicron.TrajectoryOffset, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "axis1", "1":
		return tenmicron.OffsetAxis1, true
	case "axis2", "2":
		return tenmicron.OffsetAxis2, true
	case "axis1sky", "3":
		return tenmicron.OffsetAxis1Sky, true
	case "time", "4":
		return tenmicron.OffsetTime, true
	}
	return 0, false
}

func transitStr(s tenmicron.TransitSlewState) string {
	switch s {
	case tenmicron.TransitSlewing:
		return "slewing"
	case tenmicron.TransitWaiting:
		return "waiting"
	case tenmicron.TransitCatching:
		return "catching"
	case tenmicron.TransitTracking:
		return "tracking"
	case tenmicron.TransitEnded:
		return "ended"
	}
	return "unknown"
}

func transitJSON(tr tenmicron.Transit) string {
	b, _ := json.Marshal(map[string]any{"jd_start": tr.JDStart, "jd_end": tr.JDEnd, "flip": tr.Flip})
	return string(b)
}
