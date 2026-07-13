package driver

import (
	"encoding/binary"
	"math"
	"math/rand"
)

// The star field is a fixed tile of stars several frames wide, rendered through a
// window that the pointing error scrolls across. The window wraps toroidally, so
// when tracking is off and the field drifts, stars scroll off one edge and fresh
// ones wrap in the other — the frame is never empty, and turning tracking back on
// lets the guider re-lock on whatever is currently in view. A slew re-locks the
// pointing error to zero (guideModel.reset), which recentres the window.
const (
	baseW = 1936 // reference frame, for star-density scaling only
	baseH = 1096

	// The star tile is larger than any realistic sensor, so a single frame never
	// shows a repeated asterism; the field wraps at the tile edges only when the
	// pointing error scrolls a star past them (tracking off).
	tileW = 8000.0
	tileH = 5400.0

	starsPerFrame = 7 // target multi-star density in a reference frame

	starBG    = 1200
	starSigma = 2.2
)

type tileStar struct{ x, y, peak float64 }

// tileStars is the fixed sky tile: one bright anchor at the tile origin (so a fresh
// lock always has a primary guide star at frame centre) plus a scattered field of
// varying brightness for PHD2's multi-star secondaries. The count scales with the
// tile area to hold a steady per-frame density.
var tileStars = genTileStars(int(math.Round(starsPerFrame * tileW * tileH / (baseW * baseH))))

func genTileStars(n int) []tileStar {
	r := rand.New(rand.NewSource(42))
	out := make([]tileStar, n)
	out[0] = tileStar{0, 0, 55000} // anchor: frame-centred at zero pointing error
	for i := 1; i < n; i++ {
		out[i] = tileStar{
			x:    r.Float64() * tileW,
			y:    r.Float64() * tileH,
			peak: 1500 * math.Pow(55000.0/1500.0, r.Float64()), // log-uniform 1.5k–55k ADU
		}
	}
	return out
}

// wrapDelta maps v to its nearest periodic image in [-period/2, period/2).
func wrapDelta(v, period float64) float64 {
	return v - period*math.Round(v/period)
}

// renderTile paints a w×h 16-bit mono frame (the ROI at startX,startY of a
// fullW×fullH sensor): background plus the tile's stars, windowed by the pointing
// offset (winX,winY px, toroidally wrapped) and displaced by the common-mode seeing
// offset (cmX,cmY px) and each star's per-star differential offset + intensity gain.
func renderTile(fullW, fullH, startX, startY, w, h int, winX, winY, cmX, cmY float64, pert []perturb) []byte {
	buf := make([]byte, w*h*2)
	for i := 0; i < w*h; i++ {
		binary.LittleEndian.PutUint16(buf[i*2:], starBG)
	}
	for i, st := range tileStars {
		dx, dy, gain := 0.0, 0.0, 1.0
		if i < len(pert) {
			dx, dy, gain = pert[i].dx, pert[i].dy, pert[i].gain
		}
		fx := float64(fullW)/2 + wrapDelta(st.x+winX, tileW) + cmX + dx
		fy := float64(fullH)/2 + wrapDelta(st.y+winY, tileH) + cmY + dy
		addStar(buf, w, h, fx-float64(startX), fy-float64(startY), st.peak*gain)
	}
	return buf
}

// addStar accumulates one Gaussian star of the given peak at (sx, sy) into a frame
// previously filled with starBG, clamping at 16-bit saturation.
func addStar(buf []byte, w, h int, sx, sy, peak float64) {
	cx, cy := int(sx+0.5), int(sy+0.5)
	r := int(math.Ceil(4*starSigma)) + 1
	for yy := cy - r; yy <= cy+r; yy++ {
		if yy < 0 || yy >= h {
			continue
		}
		for xx := cx - r; xx <= cx+r; xx++ {
			if xx < 0 || xx >= w {
				continue
			}
			dx, dy := float64(xx)-sx, float64(yy)-sy
			i := (yy*w + xx) * 2
			val := min(int(binary.LittleEndian.Uint16(buf[i:]))+
				int(peak*math.Exp(-(dx*dx+dy*dy)/(2*starSigma*starSigma))), 65535)
			binary.LittleEndian.PutUint16(buf[i:], uint16(val))
		}
	}
}
