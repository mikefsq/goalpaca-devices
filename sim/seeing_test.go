package driver

import (
	"math"
	"testing"
)

func rms(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x * x
	}
	return math.Sqrt(s / float64(len(xs)))
}

// TestSeeingCommonModeRMS: the common-mode term is stationary with RMS ≈ cmRMS.
func TestSeeingCommonModeRMS(t *testing.T) {
	s := newSeeing(4)
	dt := s.cmTau
	samples := make([]float64, 20000)
	for i := range samples {
		s.step(dt)
		samples[i] = s.cmX
	}
	if r := rms(samples); r < 0.8*s.cmRMS || r > 1.2*s.cmRMS {
		t.Errorf("common-mode RMS %.3f arcsec, want ~%.3f (±20%%)", r, s.cmRMS)
	}
}

// TestSeeingDifferentialIndependent: per-star jitter has the right RMS and is
// uncorrelated between stars — that independence is what multi-star averages down.
func TestSeeingDifferentialIndependent(t *testing.T) {
	s := newSeeing(4)
	dt := s.diffTau
	const n = 20000
	a := make([]float64, n)
	b := make([]float64, n)
	var dot float64
	for i := range n {
		s.step(dt)
		a[i], b[i] = s.diffX[0], s.diffX[1]
		dot += a[i] * b[i]
	}
	ra, rb := rms(a), rms(b)
	if ra < 0.8*s.diffRMS || ra > 1.2*s.diffRMS {
		t.Errorf("differential RMS %.3f arcsec, want ~%.3f", ra, s.diffRMS)
	}
	if corr := dot / (float64(n) * ra * rb); math.Abs(corr) > 0.1 {
		t.Errorf("stars 0 and 1 differential motion correlated (%.3f); should be independent", corr)
	}
}

// TestExposureAveragingReducesMotion: a longer exposure averages the fast jitter
// away, so per-frame common-mode motion RMS falls with exposure time.
func TestExposureAveragingReducesMotion(t *testing.T) {
	measure := func(exp float64) float64 {
		s := newSeeing(4)
		xs := make([]float64, 4000)
		for i := range xs {
			cmX, _, _ := s.integrate(exp)
			xs[i] = cmX
		}
		return rms(xs)
	}
	short := measure(0.01)
	long := measure(2.0)
	if long >= short {
		t.Errorf("longer exposure should reduce motion: 10ms=%.3f 2s=%.3f arcsec", short, long)
	}
	if long > 0.5*short {
		t.Errorf("2s exposure only cut motion to %.3f from %.3f; expected < half", long, short)
	}
}
