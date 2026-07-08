package driver

import (
	"encoding/json"
	"strconv"

	lx200 "github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/tenmicron"
)

// Pointing-model and alignment actions: named model slots (save/load/delete) and the
// alignment build workflow (start → add points → end), plus model telemetry. Merged
// into the action registry by actions().

func (t *Telescope) modelActions() map[string]actionFn {
	return map[string]actionFn{
		// named model slots
		"savemodel":   t.writeOnly(func(m *tenmicron.Mount, p string) error { return m.SaveModel(p) }),
		"loadmodel":   t.writeOnly(func(m *tenmicron.Mount, p string) error { return m.LoadModel(p) }),
		"deletemodel": t.writeOnly(func(m *tenmicron.Mount, p string) error { return m.DeleteModel(p) }),
		"modelcount":  t.read(func(m *tenmicron.Mount) (string, error) { n, err := m.SavedModelCount(); return strconv.Itoa(n), err }),
		"modelname":   t.indexed(func(m *tenmicron.Mount, n int) (string, error) { return m.SavedModelName(n) }),

		// alignment build workflow
		"startalignment":    t.op(func(m *tenmicron.Mount) (string, error) { return "alignment started", m.StartAlignment() }),
		"endalignment":      t.op(func(m *tenmicron.Mount) (string, error) { return "alignment ended", m.EndAlignment() }),
		"addalignmentpoint": t.op(func(m *tenmicron.Mount) (string, error) { return "point added", m.AddAlignmentPoint() }),
		"deletealignment":   t.op(func(m *tenmicron.Mount) (string, error) { return "alignment deleted", m.DeleteAlignment() }),

		// alignment telemetry
		"alignmentstarcount":  t.read(func(m *tenmicron.Mount) (string, error) { n, err := m.AlignmentStarCount(); return strconv.Itoa(n), err }),
		"maxalignmentstars":   t.read(func(m *tenmicron.Mount) (string, error) { n, err := m.MaxAlignmentStars(); return strconv.Itoa(n), err }),
		"alignmentstar":       t.indexed(func(m *tenmicron.Mount, n int) (string, error) { return m.AlignmentStar(n) }),
		"deletealignmentstar": t.indexed(func(m *tenmicron.Mount, n int) (string, error) { return "deleted", m.DeleteAlignmentStar(n) }),
		"alignmentinfo":       t.read(t.readAlignmentInfo),
		"alignmentpointinfo":  t.indexed(t.readAlignmentPointInfo),

		// programmatic point injection (plate-solved sync)
		"addalignmentspecpoint": t.actionAddAlignmentSpecPoint,
	}
}

func (t *Telescope) readAlignmentInfo(m *tenmicron.Mount) (string, error) {
	a, err := m.AlignmentInfo()
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(a)
	return string(b), nil
}

func (t *Telescope) readAlignmentPointInfo(m *tenmicron.Mount, n int) (string, error) {
	a, err := m.AlignmentPointInfo(n)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(a)
	return string(b), nil
}

// alignSpecPoint is the addalignmentspecpoint payload: a plate-solved alignment star,
// pairing the mount's raw pointing with the solved sky position. mount_side is the
// lx200.PierSide value (-1 unknown, 0 east, 1 west).
type alignSpecPoint struct {
	MountRA      float64 `json:"mount_ra"`
	MountDec     float64 `json:"mount_dec"`
	MountSide    int     `json:"mount_side"`
	SolvedRA     float64 `json:"solved_ra"`
	SolvedDec    float64 `json:"solved_dec"`
	SiderealTime float64 `json:"sidereal_time"`
}

// actionAddAlignmentSpecPoint injects a fully-specified alignment point and returns the
// new point's index. Payload is JSON (see alignSpecPoint).
func (t *Telescope) actionAddAlignmentSpecPoint(params string) (string, error) {
	if params == "" {
		return "", badValue("addalignmentspecpoint: JSON payload required")
	}
	var p alignSpecPoint
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		return "", badValue("addalignmentspecpoint: invalid JSON: " + err.Error())
	}
	m, err := t.live()
	if err != nil {
		return "", err
	}
	n, err := m.AddAlignmentSpecPoint(tenmicron.AlignmentPoint{
		MountRA:      p.MountRA,
		MountDec:     p.MountDec,
		MountSide:    lx200.PierSide(p.MountSide),
		SolvedRA:     p.SolvedRA,
		SolvedDec:    p.SolvedDec,
		SiderealTime: p.SiderealTime,
	})
	if err != nil {
		return "", err
	}
	return strconv.Itoa(n), nil
}
