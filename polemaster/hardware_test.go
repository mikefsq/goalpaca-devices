package driver

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// TestAlpacaHardware exercises HTTP, the driver, and USB with a real camera.
// Opt in with POLEMASTER_HARDWARE=1; POLEMASTER_FIRMWARE selects a custom image.
// The test changes camera settings and captures images. Keep the scene still
// and detailed for the crop comparison; cleanup closes the server and camera.
//
//	POLEMASTER_HARDWARE=1 go test -run '^TestAlpacaHardware$' -count=1 -v ./...
func TestAlpacaHardware(t *testing.T) {
	if os.Getenv("POLEMASTER_HARDWARE") == "" {
		t.Skip("set POLEMASTER_HARDWARE=1 with a PoleMaster attached")
	}

	dev := NewPoleMaster(os.Getenv("POLEMASTER_FIRMWARE"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := dev.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close(context.Background()) })

	srv := alpacadev.New(alpacadev.Config{
		Discovery:  alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName: "polemaster-hw", Manufacturer: "test",
	})
	if err := srv.Register(alpacadev.CameraType, 0, dev); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	base := ts.URL + "/api/v1/camera/0/"

	waitConnected(t, base)

	t.Logf("name=%v sensor=%v type=%v", hwValue(t, base, "name"),
		hwValue(t, base, "sensorname"), hwValue(t, base, "sensortype"))
	t.Logf("size=%vx%v pixel=%vx%v um", hwValue(t, base, "cameraxsize"),
		hwValue(t, base, "cameraysize"), hwValue(t, base, "pixelsizex"),
		hwValue(t, base, "pixelsizey"))
	t.Logf("gain=%v..%v offset=%v..%v", hwValue(t, base, "gainmin"),
		hwValue(t, base, "gainmax"), hwValue(t, base, "offsetmin"),
		hwValue(t, base, "offsetmax"))
	t.Logf("exposure=%v..%v res=%v", hwValue(t, base, "exposuremin"),
		hwValue(t, base, "exposuremax"), hwValue(t, base, "exposureresolution"))
	t.Logf("readoutmodes=%v maxadu=%v", hwValue(t, base, "readoutmodes"),
		hwValue(t, base, "maxadu"))

	// Advertised capabilities must match the supported operations.
	for member, want := range map[string]any{
		"cansetccdtemperature": false,
		"cangetcoolerpower":    false,
		"canpulseguide":        false,
		"hasshutter":           false,
		"canstopexposure":      false,
		"canabortexposure":     true,
		"maxbinx":              float64(1),
		"maxbiny":              float64(1),
	} {
		if got := hwGet(t, base, member).Value; got != want {
			t.Errorf("%s = %v, want %v", member, got, want)
		}
	}

	if r := hwPut(t, base, "gain", "Gain=5"); r.ErrorNumber != 0 {
		t.Fatalf("set gain: %s", r.ErrorMessage)
	}
	if r := hwPut(t, base, "offset", "Offset=0"); r.ErrorNumber != 0 {
		t.Fatalf("set offset: %s", r.ErrorMessage)
	}

	// A subframe whose dimensions the sensor cannot read directly: odd in both
	// axes, at an odd origin, and not a whole number of packets. The driver has
	// to read a legal window around it and crop.
	t.Run("awkward subframe", func(t *testing.T) {
		const sx, sy, w, h = 33, 45, 101, 61
		setSubframe(t, base, sx, sy, w, h)
		expose(t, base, 0.002)
		img := imageArray(t, base)
		// ASCOM ImageArray is column-major: Value[x][y].
		if len(img) != w {
			t.Fatalf("image has %d columns, want %d", len(img), w)
		}
		if len(img[0]) != h {
			t.Fatalf("image has %d rows, want %d", len(img[0]), h)
		}
		lo, hi := img[0][0], img[0][0]
		for _, col := range img {
			for _, v := range col {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		t.Logf("subframe %dx%d+%d+%d: min %d max %d", w, h, sx, sy, lo, hi)
		if hi > 255 {
			t.Errorf("eight-bit frame carries %d, past 255", hi)
		}
		if lo == hi {
			t.Errorf("every pixel is %d; the frame carries no image", lo)
		}
	})

	t.Run("twelve bits", func(t *testing.T) {
		if r := hwPut(t, base, "readoutmode", "ReadoutMode=1"); r.ErrorNumber != 0 {
			t.Fatalf("set readout mode: %s", r.ErrorMessage)
		}
		t.Cleanup(func() { hwPut(t, base, "readoutmode", "ReadoutMode=0") })
		if got := toI(hwGet(t, base, "maxadu").Value); got != 4095 {
			t.Errorf("maxadu = %d at twelve bits, want 4095", got)
		}
		const w, h = 64, 48
		setSubframe(t, base, 0, 0, w, h)
		expose(t, base, 0.002)
		img := imageArray(t, base)
		if len(img) != w || len(img[0]) != h {
			t.Fatalf("image is %dx%d, want %dx%d", len(img), len(img[0]), w, h)
		}
		lo, hi := img[0][0], img[0][0]
		for _, col := range img {
			for _, v := range col {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		t.Logf("twelve-bit %dx%d: min %d max %d", w, h, lo, hi)
		if hi > 4095 {
			t.Errorf("twelve-bit frame carries %d, past 4095", hi)
		}
	})

	// Compare a detailed region in separate full-frame and subframe captures.
	// Skip flat scenes: they cannot distinguish a correct crop from a shifted one.
	t.Run("crop lands where it should", func(t *testing.T) {
		if r := hwPut(t, base, "gain", "Gain=10"); r.ErrorNumber != 0 {
			t.Fatalf("set gain: %s", r.ErrorMessage)
		}
		t.Cleanup(func() { hwPut(t, base, "gain", "Gain=5") })

		setSubframe(t, base, 0, 0, 1280, 960)
		expose(t, base, 0.002)
		full := imageArray(t, base)

		const iw, ih = 61, 41
		bx, by, sd := busiestTile(full, iw, ih)
		t.Logf("busiest %dx%d region is at (%d,%d), sd %.2f", iw, ih, bx, by, sd)
		if sd < 4 {
			t.Skipf("the whole scene is flat (best sd %.2f); point the camera at "+
				"something with detail in it to check crop placement", sd)
		}

		setSubframe(t, base, bx, by, iw, ih)
		expose(t, base, 0.002)
		inner := imageArray(t, base)
		if len(inner) != iw || len(inner[0]) != ih {
			t.Fatalf("subframe is %dx%d, want %dx%d", len(inner), len(inner[0]), iw, ih)
		}

		if os.Getenv("POLEMASTER_OFFSET_SCAN") != "" {
			for ddy := -2; ddy <= 2; ddy++ {
				var row []string
				for ddx := -2; ddx <= 2; ddx++ {
					row = append(row, ftoa2(meanAbsDiff(full, inner, bx+ddx, by+ddy)))
				}
				t.Logf("dy=%+d  %s", ddy, strings.Join(row, "  "))
			}
		}
		aligned := meanAbsDiff(full, inner, bx, by)
		near := meanAbsDiff(full, inner, bx+1, by+1)
		far := meanAbsDiff(full, inner, bx+16, by+16)
		t.Logf("mean |diff| aligned %.2f, 1px out %.2f, 16px out %.2f", aligned, near, far)

		if aligned >= far {
			t.Errorf("the subframe matches 16 pixels away at least as well "+
				"(%.2f aligned, %.2f shifted): the crop is misplaced", aligned, far)
		}
		if aligned >= near {
			t.Logf("note: a one-pixel shift matches as well (%.2f vs %.2f); "+
				"this scene cannot resolve single-pixel placement", aligned, near)
		}
	})

	t.Run("full frame", func(t *testing.T) {
		setSubframe(t, base, 0, 0, 1280, 960)
		start := time.Now()
		expose(t, base, 0.002)
		t.Logf("full frame took %v", time.Since(start).Round(time.Millisecond))
		if got := toI(hwGet(t, base, "numx").Value); got != 1280 {
			t.Errorf("numx = %d after a full-frame exposure", got)
		}
		if d := hwGet(t, base, "lastexposureduration").Value; d == nil {
			t.Error("lastexposureduration is unset after an exposure")
		} else {
			t.Logf("lastexposureduration=%v starttime=%v", d,
				hwValue(t, base, "lastexposurestarttime"))
		}
	})
}

func setSubframe(t *testing.T, base string, x, y, w, h int) {
	t.Helper()
	for member, form := range map[string]string{
		"startx": "StartX=" + itoa(x), "starty": "StartY=" + itoa(y),
		"numx": "NumX=" + itoa(w), "numy": "NumY=" + itoa(h),
	} {
		if r := hwPut(t, base, member, form); r.ErrorNumber != 0 {
			t.Fatalf("set %s: %s", member, r.ErrorMessage)
		}
	}
}

// expose starts one exposure and waits for the image, failing on a camera that
// reports an error state rather than hanging until the deadline.
func expose(t *testing.T, base string, secs float64) {
	t.Helper()
	r := hwPut(t, base, "startexposure", "Duration="+ftoa(secs)+"&Light=true")
	if r.ErrorNumber != 0 {
		t.Fatalf("startexposure: %s", r.ErrorMessage)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if hwGet(t, base, "imageready").Value == true {
			return
		}
		if toI(hwGet(t, base, "camerastate").Value) == int(alpacadev.CameraError) {
			t.Fatal("the camera went to its error state during the exposure")
		}
		if time.Now().After(deadline) {
			t.Fatalf("no image after 60s (state %v, %v%% complete)",
				hwValue(t, base, "camerastate"), hwValue(t, base, "percentcompleted"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// imageArray fetches the image as ASCOM's column-major Value[x][y].
func imageArray(t *testing.T, base string) [][]int {
	t.Helper()
	r, err := http.Get(base + "imagearray?" + hwTxQ)
	if err != nil {
		t.Fatalf("GET imagearray: %v", err)
	}
	defer r.Body.Close()
	var out struct {
		Value        [][]int
		ErrorNumber  int
		ErrorMessage string
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatalf("decode imagearray: %v", err)
	}
	if out.ErrorNumber != 0 {
		t.Fatalf("imagearray: %s", out.ErrorMessage)
	}
	if len(out.Value) == 0 {
		t.Fatal("imagearray came back empty")
	}
	return out.Value
}

const hwTxQ = "ClientID=1&ClientTransactionID=1"

type hwResp struct {
	Value        any
	ErrorNumber  int
	ErrorMessage string
}

func hwGet(t *testing.T, base, member string) hwResp {
	t.Helper()
	r, err := http.Get(base + member + "?" + hwTxQ)
	if err != nil {
		t.Fatalf("GET %s: %v", member, err)
	}
	defer r.Body.Close()
	var out hwResp
	json.NewDecoder(r.Body).Decode(&out)
	return out
}

func hwPut(t *testing.T, base, member, form string) hwResp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+member, strings.NewReader(form+"&"+hwTxQ))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", member, err)
	}
	defer r.Body.Close()
	var out hwResp
	json.NewDecoder(r.Body).Decode(&out)
	return out
}

func hwValue(t *testing.T, base, member string) any {
	t.Helper()
	return hwGet(t, base, member).Value
}

func waitConnected(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if hwGet(t, base, "connected").Value == true {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the camera never came up (none attached?)")
}

func toI(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func itoa(n int) string     { return strings.TrimSpace(jsonNum(float64(n), 0)) }
func ftoa(f float64) string { return jsonNum(f, 6) }

func jsonNum(f float64, prec int) string {
	b, _ := json.Marshal(f)
	s := string(b)
	if prec == 0 && strings.Contains(s, ".") {
		s = s[:strings.Index(s, ".")]
	}
	return s
}

// Compare inner against outer at origin (dx, dy), using column-major arrays.
func meanAbsDiff(outer, inner [][]int, dx, dy int) float64 {
	var sum, n float64
	for x := range inner {
		for y := range inner[x] {
			ox, oy := x+dx, y+dy
			if ox >= len(outer) || oy >= len(outer[ox]) {
				continue
			}
			d := outer[ox][oy] - inner[x][y]
			if d < 0 {
				d = -d
			}
			sum += float64(d)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// Measure contrast to detect scenes unsuitable for alignment checks.
func stdDev(img [][]int) float64 {
	var sum, n float64
	for _, col := range img {
		for _, v := range col {
			sum += float64(v)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	var sq float64
	for _, col := range img {
		for _, v := range col {
			d := float64(v) - mean
			sq += d * d
		}
	}
	return math.Sqrt(sq / n)
}

// Find the highest-contrast tile on a coarse grid; an exact maximum is unnecessary.
func busiestTile(img [][]int, w, h int) (int, int, float64) {
	const step = 64
	bx, by, best := 0, 0, -1.0
	for x := 0; x+w <= len(img); x += step {
		for y := 0; y+h <= len(img[x]); y += step {
			if sd := tileStdDev(img, x, y, w, h); sd > best {
				bx, by, best = x, y, sd
			}
		}
	}
	return bx, by, best
}

func tileStdDev(img [][]int, x0, y0, w, h int) float64 {
	var sum float64
	n := float64(w * h)
	for x := x0; x < x0+w; x++ {
		for y := y0; y < y0+h; y++ {
			sum += float64(img[x][y])
		}
	}
	mean := sum / n
	var sq float64
	for x := x0; x < x0+w; x++ {
		for y := y0; y < y0+h; y++ {
			d := float64(img[x][y]) - mean
			sq += d * d
		}
	}
	return math.Sqrt(sq / n)
}

func ftoa2(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
