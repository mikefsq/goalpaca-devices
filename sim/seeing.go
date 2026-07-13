package driver

import (
	"math"
	"math/rand"
	"sync"
)

// seeing is the atmospheric overlay applied by the camera at render time (seeing
// lives in the image, not in the mount's pointing). Three Ornstein-Uhlenbeck
// (exponentially-correlated) terms, all in arc-seconds so the camera's single
// pixel-scale conversion covers them alongside the pointing error:
//
//   - common-mode image motion: all stars tip/tilt together (the guider chases it).
//   - per-star differential motion: each star jitters independently (~0.3×), the
//     term PHD2's multi-star centroid averages down ~1/√N.
//   - per-star scintillation: independent log-normal intensity flicker.
//
// integrate averages each term over the exposure so a longer exposure smooths the
// fast jitter (RMS ∝ T^-0.5 past the correlation time), matching real guiding.
type seeing struct {
	mu  sync.Mutex
	rng *rand.Rand

	cmRMS, cmTau       float64 // common-mode image motion, arcsec / s
	diffRMS, diffTau   float64 // per-star differential motion, arcsec / s
	scintRMS, scintTau float64 // per-star log-intensity fluctuation, - / s

	cmX, cmY     float64
	diffX, diffY []float64 // per tile-star (stable index)
	scint        []float64
}

const (
	seeingSubDt  = 0.0025 // s (≈ scintTau/4): OU integration step for exposure averaging
	seeingMaxSub = 4096
)

func newSeeing(nStars int) *seeing {
	return &seeing{
		// Fixed seed keeps the seeing series reproducible across runs (a testbed
		// feature); reseed rng for run-to-run variety.
		rng:      rand.New(rand.NewSource(1)),
		cmRMS:    0.5, // arcsec RMS common-mode (≈0.25px at ~2"/px)
		cmTau:    0.05,
		diffRMS:  0.15, // ~0.3× common-mode
		diffTau:  0.05,
		scintRMS: 0.08, // ~8% intensity flicker
		scintTau: 0.01,
		diffX:    make([]float64, nStars),
		diffY:    make([]float64, nStars),
		scint:    make([]float64, nStars),
	}
}

// ou advances one Ornstein-Uhlenbeck coordinate by dt (exact for any dt).
func (s *seeing) ou(v, sigma, tau, dt float64) float64 {
	if sigma <= 0 || tau <= 0 {
		return 0
	}
	a := math.Exp(-dt / tau)
	return v*a + sigma*math.Sqrt(1-a*a)*s.rng.NormFloat64()
}

// step advances every seeing OU process by dt seconds (no averaging).
func (s *seeing) step(dt float64) {
	s.cmX = s.ou(s.cmX, s.cmRMS, s.cmTau, dt)
	s.cmY = s.ou(s.cmY, s.cmRMS, s.cmTau, dt)
	for i := range s.diffX {
		s.diffX[i] = s.ou(s.diffX[i], s.diffRMS, s.diffTau, dt)
		s.diffY[i] = s.ou(s.diffY[i], s.diffRMS, s.diffTau, dt)
		s.scint[i] = s.ou(s.scint[i], s.scintRMS, s.scintTau, dt)
	}
}

// perturb is one star's per-frame seeing: differential offset (arcsec) and a
// multiplicative intensity gain.
type perturb struct {
	dx, dy, gain float64
}

// integrate steps the seeing over an exposure of expSecs and returns the time-
// averaged common-mode offset (arcsec) plus per-star perturbations, so a longer
// exposure averages more of the fast jitter away.
func (s *seeing) integrate(expSecs float64) (cmX, cmY float64, p []perturb) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := int(math.Ceil(expSecs / seeingSubDt))
	switch {
	case n < 1:
		n = 1
	case n > seeingMaxSub:
		n = seeingMaxSub
	}
	dt := expSecs / float64(n)

	nStars := len(s.diffX)
	sumDx := make([]float64, nStars)
	sumDy := make([]float64, nStars)
	sumGain := make([]float64, nStars)
	for range n {
		s.step(dt)
		cmX += s.cmX
		cmY += s.cmY
		for i := range nStars {
			sumDx[i] += s.diffX[i]
			sumDy[i] += s.diffY[i]
			sumGain[i] += math.Exp(s.scint[i])
		}
	}
	inv := 1.0 / float64(n)
	p = make([]perturb, nStars)
	for i := range p {
		p[i] = perturb{dx: sumDx[i] * inv, dy: sumDy[i] * inv, gain: sumGain[i] * inv}
	}
	return cmX * inv, cmY * inv, p
}
