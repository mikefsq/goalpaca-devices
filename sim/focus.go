package driver

import (
	"math"
	"sync"
)

// Focus model: star Gaussian sigma grows as a hyperbola about focusBest, so an AF run gives a V
// curve. sigma = sqrt(starSigma² + (focusSlope·(pos-focusBest))²); ±1000 steps ≈ 5× in-focus size.
const (
	focusBest  = 10000
	focusSlope = 0.011 // px of sigma per step, far from focus
)

// focus couples the sim-focuser to every sim-camera, like sky does the mount.
var focus = &focusModel{}

type focusModel struct {
	mu  sync.Mutex
	pos func() (int, error) // nil until a sim focuser binds: stars stay in focus
}

// bind wires the model to a focuser's live position.
func (m *focusModel) bind(pos func() (int, error)) {
	m.mu.Lock()
	m.pos = pos
	m.mu.Unlock()
}

// sigma returns star Gaussian sigma (px) at the focuser's current position.
func (m *focusModel) sigma() float64 {
	m.mu.Lock()
	pos := m.pos
	m.mu.Unlock()
	if pos == nil {
		return starSigma
	}
	p, err := pos()
	if err != nil {
		return starSigma
	}
	d := focusSlope * float64(p-focusBest)
	return math.Sqrt(starSigma*starSigma + d*d)
}
