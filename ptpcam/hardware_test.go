//go:build hardware

package ptpcam

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/fuji"
)

// An end-to-end capture on real hardware, over real HTTP, with the frame never
// touching disk.
//
//	pkill -9 ptpcamerad && go test -tags hardware -run Hardware -v
//
// What this covers that nothing else does is the TRANSPORT: that the samples
// survive ImageFrame, the Rank-2 column-major transposition in the ImageBytes
// encoder, and HTTP. The DECODER is verified separately and more strongly, by
// cmd/cfaverify against libraw's unprocessed_raw on real frames — so comparing
// here against the decoder itself is the right check, not a circular one.
//
// The disk assertion is a directory snapshot, which proves no file was created
// where one would be. It is not a syscall trace, so it cannot prove nothing was
// written anywhere at all — but the path from Download to ImageBytes has no
// file I/O in it, and this catches a regression that introduced some.
func TestHardwareCaptureNeverTouchesDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	opener := func() (ptp.Camera, error) {
		cam, err := fuji.Open("")
		if err != nil {
			return nil, err
		}
		// RAW only, losslessly compressed: the format the decoder exists for.
		if err := cam.SetQuality(fuji.QualityRaw); err != nil {
			cam.Close()
			return nil, fmt.Errorf("setting RAW quality: %w", err)
		}
		if err := cam.SetRawCompression(fuji.RawLossless); err != nil {
			cam.Close()
			return nil, fmt.Errorf("setting lossless RAW: %w", err)
		}
		return cam, nil
	}

	cam := New("ptpcam-0", "X-T5", opener)
	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:   11199,
		Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:   "ptpcam-hwtest",
		Manufacturer: "mikefsq",
	})
	if err := srv.Register(alpacadev.CameraType, 0, cam); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	base := "http://127.0.0.1:11199/api/v1/camera/0/"
	waitFor(t, 20*time.Second, func() bool { return getBool(t, base+"connected") })
	t.Log("camera connected")

	// Poll fast: a 200ms tick adds up to 200ms of pure latency to every wait,
	// and the point of this run is to find where the seconds go.
	t0 := time.Now()
	put(t, base+"startexposure", "Duration=0.4&Light=True")
	tPut := time.Now()
	// A failed exposure must not look like a slow one: the state machine leaves
	// CameraIdle without ImageReady, and polling only ImageReady would sit here
	// until the timeout with no idea why.
	waitForFast(t, 60*time.Second, func() bool {
		if getBool(t, base+"imageready") {
			return true
		}
		if cam.CameraState() == alpacadev.CameraError {
			t.Fatalf("the exposure failed; camera state is Error")
		}
		return false
	})
	tReady := time.Now()
	t.Logf("TIMING startexposure returned  %v", tPut.Sub(t0).Round(time.Millisecond))
	t.Logf("TIMING imageready              %v (exposure 400ms is inside this)", tReady.Sub(tPut).Round(time.Millisecond))

	// Pull the frame as ImageBytes — the binary Alpaca transport.
	req, _ := http.NewRequest("GET", base+"imagearray", nil)
	req.Header.Set("Accept", alpaca.ImageBytesMIME)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	tWire := time.Now()
	t.Logf("TIMING imagearray over HTTP    %v for %d bytes (%.0f MB/s)",
		tWire.Sub(tReady).Round(time.Millisecond), len(wire),
		float64(len(wire))/tWire.Sub(tReady).Seconds()/1e6)
	t.Logf("TIMING total shutter to bytes  %v", tWire.Sub(t0).Round(time.Millisecond))

	frame, err := alpaca.DecodeImageBytes(wire)
	if err != nil {
		t.Fatalf("DecodeImageBytes: %v", err)
	}
	if frame.Rank != 2 || frame.ElementType != alpaca.ImgUInt16 {
		t.Errorf("rank %d type %v, want a Rank-2 16-bit readout", frame.Rank, frame.ElementType)
	}
	t.Logf("frame %dx%d, %d samples", frame.Width, frame.Height, frame.Width*frame.Height)

	// The samples must survive the transport unchanged.
	raf, _, ok := cam.LastFile()
	if !ok {
		t.Fatal("the driver kept no file")
	}
	t.Logf("RAF held in memory: %d bytes", len(raf))
	want, err := fuji.DecodeRAF(raf)
	if err != nil {
		t.Fatalf("decoding the held bytes: %v", err)
	}
	diff := 0
	for i := range want.Pixels {
		if binary.LittleEndian.Uint16(frame.Pixels[i*2:]) != want.Pixels[i] {
			diff++
		}
	}
	if diff != 0 {
		t.Fatalf("%d of %d samples were altered in transport", diff, len(want.Pixels))
	}
	t.Logf("all %d samples survived ImageFrame -> ImageBytes -> HTTP", len(want.Pixels))

	if after := snapshot(t, dir); len(after) != len(before) {
		t.Errorf("files appeared on disk during the capture: %v", after)
	} else {
		t.Log("no files were written")
	}
}

func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func waitForFast(t *testing.T, bound time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", bound)
}

func waitFor(t *testing.T, bound time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", bound)
}

func getBool(t *testing.T, url string) bool {
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return containsTrue(string(b))
}

func containsTrue(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == "true" {
			return true
		}
	}
	return false
}

func put(t *testing.T, url, body string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", url, stringReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	t.Logf("PUT %s -> %s", url, string(b))
}

func stringReader(s string) *stringRdr { return &stringRdr{s: s} }

type stringRdr struct {
	s string
	i int
}

func (r *stringRdr) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}
