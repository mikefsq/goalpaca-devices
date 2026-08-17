package ptpcam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikefsq/ptp"
)

// fakeBody is a camera that satisfies the parent package's capability
// interfaces without any hardware. It is the reason those interfaces exist: the
// driver holds ptp.Capturer and friends, not a Fujifilm or Sony type, so a test
// can stand in for either body.
type fakeBody struct {
	shutter   time.Duration
	frame     []byte
	filename  string
	handed    bool // Close was called, i.e. the camera was handed back
	deleted   []uint32
	served    bool     // NewFrames has already reported the frame
	handles   []uint32 // what NewFrames reports; defaults to one frame
	delErr    error    // make Delete fail, to test a filling buffer
	downloads int      // how many frames actually crossed the wire
	live      []byte
	liveErr   error
}

func (f *fakeBody) Model() string { return "Fake X-1" }
func (f *fakeBody) Close() error  { f.handed = true; return nil }

func (f *fakeBody) Capture(timeout time.Duration) error { f.served = false; return nil }

// SetShutter snaps to a ladder, exactly as a real body does: this fake only
// offers 1/1000, 1/60, 1s and 4s.
func (f *fakeBody) SetShutter(d time.Duration) error {
	rungs := []time.Duration{time.Second / 1000, time.Second / 60, time.Second, 4 * time.Second}
	best := rungs[0]
	for _, r := range rungs {
		if abs(int64(r-d)) < abs(int64(best-d)) {
			best = r
		}
	}
	f.shutter = best
	return nil
}
func (f *fakeBody) SetAperture(float64) error { return nil }
func (f *fakeBody) SetISO(uint32) error       { return nil }
func (f *fakeBody) Exposure() (*ptp.Exposure, error) {
	return &ptp.Exposure{Shutter: f.shutter}, nil
}

func (f *fakeBody) NewFrames() ([]uint32, error) {
	if f.served {
		return nil, nil
	}
	f.served = true
	if f.handles != nil {
		return f.handles, nil
	}
	return []uint32{0x42}, nil
}
func (f *fakeBody) Download(h uint32) ([]byte, string, error) {
	f.downloads++
	return f.frame, f.filename, nil
}
func (f *fakeBody) Delete(h uint32) error {
	f.deleted = append(f.deleted, h)
	return f.delErr
}
func (f *fakeBody) SetManualFocus() error      { return nil }
func (f *fakeBody) LiveFrame() ([]byte, error) { return f.live, f.liveErr }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// The fake must satisfy the same interfaces a real body does, or the test is
// not exercising the driver's actual path.
var (
	_ ptp.Camera          = (*fakeBody)(nil)
	_ ptp.Capturer        = (*fakeBody)(nil)
	_ ptp.ExposureControl = (*fakeBody)(nil)
	_ ptp.Downloader      = (*fakeBody)(nil)
	_ ptp.FocusControl    = (*fakeBody)(nil)
	_ ptp.LiveViewer      = (*fakeBody)(nil)
)

// testJPEG builds a JPEG of a known size and colour.
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding the test JPEG: %v", err)
	}
	return buf.Bytes()
}

// openFake starts the driver and waits for the supervisor to acquire the fake
// body. Open is asynchronous by design — the endpoint must come up with no
// camera attached — so a caller waits for Connected exactly as a client does.
func openFake(t *testing.T, f *fakeBody) *Camera {
	t.Helper()
	c := New("test-id", "Fake", func() (ptp.Camera, error) { return f, nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	waitConnected(t, c)
	return c
}

func waitConnected(t *testing.T, c *Camera) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Connected() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the supervisor did not acquire the camera within 5s")
}

// waitIdle polls as an Alpaca client does, rather than sleeping a guessed
// interval.
func waitIdle(t *testing.T, c *Camera) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.ImageReady() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no image after 5s; camera state %v", c.CameraState())
}

func TestExposureProducesAnImageFrame(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "DSCF0001.JPG"}
	c := openFake(t, f)

	if err := c.StartExposure(0.01, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	waitIdle(t, c)

	frame, err := c.ImageFrame()
	if err != nil {
		t.Fatalf("ImageFrame: %v", err)
	}
	if frame.Rank != 3 || frame.Planes != 3 {
		t.Errorf("rank %d planes %d, want a 3-plane RGB frame", frame.Rank, frame.Planes)
	}
	if frame.Width != 64 || frame.Height != 48 {
		t.Errorf("frame is %dx%d, want 64x48", frame.Width, frame.Height)
	}
	if got, want := len(frame.Pixels), 64*48*3; got != want {
		t.Errorf("%d pixel bytes, want %d", got, want)
	}
}

// The delivered frame's geometry MUST match CameraXSize/NumX, or a client
// renders a sheared image.
func TestGeometryMatchesTheDeliveredFrame(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "x.jpg"}
	c := openFake(t, f)
	c.StartExposure(0.01, true)
	waitIdle(t, c)

	if c.CameraXSize() != 64 || c.CameraYSize() != 48 {
		t.Errorf("camera size %dx%d, want 64x48", c.CameraXSize(), c.CameraYSize())
	}
	if c.NumX() != c.CameraXSize() || c.NumY() != c.CameraYSize() {
		t.Error("NumX/NumY disagree with the sensor size")
	}
}

// The camera's ladder, not the request, is what was actually shot. Reporting
// the request would be a fiction a client might average over.
func TestLastExposureDurationReportsTheLadderRung(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}
	c := openFake(t, f)

	// 0.9s is not a rung; the nearest is 1s.
	if err := c.StartExposure(0.9, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	waitIdle(t, c)

	got, err := c.LastExposureDuration()
	if err != nil {
		t.Fatalf("LastExposureDuration: %v", err)
	}
	if got != 1.0 {
		t.Errorf("reported %gs, want 1s — the rung actually used, not the 0.9s requested", got)
	}
}

// A frame left on the camera is not a leak, it is a lockup: a Fujifilm body
// holds about five and refuses to hand back control while any remain.
func TestFrameIsDeletedFromTheCamera(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}
	c := openFake(t, f)
	c.StartExposure(0.01, true)
	waitIdle(t, c)

	if len(f.deleted) != 1 || f.deleted[0] != 0x42 {
		t.Errorf("deleted %v, want the captured frame removed", f.deleted)
	}
}

// A RAW-only capture cannot go through ImageFrame, but the file is still good.
// Losing it because it will not decode would throw away the actual photograph.
func TestRawFrameIsKeptEvenThoughItCannotDecode(t *testing.T) {
	f := &fakeBody{frame: []byte("FUJIFILMCCD-RAW not a jpeg"), filename: "DSCF0002.RAF"}
	c := openFake(t, f)
	c.StartExposure(0.01, true)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := c.LastFile(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, name, ok := c.LastFile()
	if !ok {
		t.Fatal("the RAW frame was discarded because it would not decode")
	}
	if !bytes.Equal(data, f.frame) {
		t.Error("the file was modified; it must be byte-for-byte what the camera produced")
	}
	if name != "DSCF0002.RAF" {
		t.Errorf("filename %q, want the camera's own", name)
	}
	if c.ImageReady() {
		t.Error("ImageReady is true though no frame could be decoded")
	}
}

// A RAW frame is kept EXACTLY as the camera produced it even though nothing can
// currently serve it: throwing away the actual photograph because ImageFrame
// cannot decode it would be the wrong failure.
func TestLastFileKeepsTheFileUnmodified(t *testing.T) {
	raw := []byte("FUJIFILMCCD-RAW\x00\x01\x02\x03")
	f := &fakeBody{frame: raw, filename: "DSCF0003.RAF"}
	c := openFake(t, f)
	c.StartExposure(0.01, true)
	for i := 0; i < 200; i++ {
		if _, _, ok := c.LastFile(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	data, name, ok := c.LastFile()
	if !ok {
		t.Fatal("no file was kept")
	}
	if !bytes.Equal(data, raw) {
		t.Error("the kept bytes differ from what the camera produced")
	}
	if name != "DSCF0003.RAF" {
		t.Errorf("filename %q, want the camera's own", name)
	}
}

func TestLastFileBeforeAnyCapture(t *testing.T) {
	c := openFake(t, &fakeBody{})
	if _, _, ok := c.LastFile(); ok {
		t.Error("LastFile reports a frame before anything has been captured")
	}
}

func TestLiveFrameIsReturnedUnmodified(t *testing.T) {
	live := testJPEG(t, 640, 480)
	c := openFake(t, &fakeBody{live: live})

	got, err := c.LiveFrame()
	if err != nil {
		t.Fatalf("LiveFrame: %v", err)
	}
	if !bytes.Equal(got, live) {
		t.Error("the preview was modified on the way out")
	}
}

// Closing must hand the camera back. A Fujifilm body left in PC Priority has
// its dials and shutter dead in its owner's hands.
func TestCloseHandsTheCameraBack(t *testing.T) {
	f := &fakeBody{}
	c := openFake(t, f)
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !f.handed {
		t.Error("Close did not close the body")
	}
}

// A dark frame is not something these bodies can take. Saying so is better than
// silently returning a light frame labelled as a dark.
func TestDarkFrameIsRefused(t *testing.T) {
	c := openFake(t, &fakeBody{frame: testJPEG(t, 16, 16)})
	if err := c.StartExposure(0.01, false); err == nil {
		t.Error("a dark frame was accepted; this camera cannot take one")
	}
}

func TestSecondExposureWhileBusyIsRefused(t *testing.T) {
	c := openFake(t, &fakeBody{frame: testJPEG(t, 16, 16)})
	c.mu.Lock()
	c.exposing = true
	c.mu.Unlock()
	if err := c.StartExposure(0.01, true); err == nil {
		t.Error("a second exposure was accepted while one was running")
	}
}

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w, h int
		ok   bool
	}{
		{"7728x5152", 7728, 5152, true},
		{"4096X3072", 4096, 3072, true},
		{" 640 x 480 ", 640, 480, true},
		{"Large", 0, 0, false}, // Sony's L/M/S enum, not a size
		{"", 0, 0, false},
		{"1920x", 0, 0, false},
	} {
		w, h, ok := parseSize(tc.in)
		if ok != tc.ok || w != tc.w || h != tc.h {
			t.Errorf("parseSize(%q) = %d,%d,%v want %d,%d,%v", tc.in, w, h, ok, tc.w, tc.h, tc.ok)
		}
	}
}

// ---------------------------------------------------------------- lifecycle

// The endpoint must come up with NO camera attached. On a fleet box the service
// starts at boot and the body is very likely powered off at that moment; an
// Open that failed would take the whole device down.
func TestOpenSucceedsWithNoCameraAttached(t *testing.T) {
	c := New("test-id", "Fake", func() (ptp.Camera, error) {
		return nil, errors.New("no camera attached")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Open(ctx); err != nil {
		t.Fatalf("Open failed with no camera: %v — the Alpaca device would not start", err)
	}
	if c.Connected() {
		t.Error("Connected is true though no camera was acquired")
	}
	if err := c.Connect(context.Background()); err == nil {
		t.Error("Connect succeeded with no camera attached")
	}
	// Members must report absence, not a zero value that reads like data.
	if err := c.StartExposure(0.01, true); err == nil {
		t.Error("StartExposure was accepted with no camera")
	}
}

// The camera is very likely powered on AFTER the service starts. The supervisor
// must pick it up without anyone restarting anything.
func TestCameraAppearingLaterIsAcquired(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}
	var present atomic.Bool // the body is not plugged in yet

	c := New("test-id", "Fake", func() (ptp.Camera, error) {
		if !present.Load() {
			return nil, errors.New("no camera attached")
		}
		return f, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Open(ctx)

	if c.Connected() {
		t.Fatal("connected before the camera was plugged in")
	}
	present.Store(true) // the operator powers the body on
	waitConnected(t, c)

	// And it is usable, not merely present.
	if err := c.StartExposure(0.01, true); err != nil {
		t.Fatalf("StartExposure after acquisition: %v", err)
	}
	waitIdle(t, c)
}

// A camera that is unplugged must be noticed and released, so the session does
// not sit there costing a full USB timeout on every request.
func TestCameraGoingAwayIsReleased(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}
	var present atomic.Bool
	present.Store(true)

	// A camera that is gone fails BOTH ways: the presence probe cannot see it,
	// and opening it fails too. Modelling only one of those would let the
	// supervisor re-acquire a body that is not there.
	c := New("test-id", "Fake", func() (ptp.Camera, error) {
		if !present.Load() {
			return nil, errors.New("no camera")
		}
		return f, nil
	})
	c.AliveFn = present.Load
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Open(ctx)
	waitConnected(t, c)

	present.Store(false) // the body is powered off

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && c.Connected() {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Connected() {
		t.Fatal("the camera was still reported connected after it went away")
	}
	if !f.handed {
		t.Error("the body was not closed when it went away")
	}
}

// Power-cycled: off, then on again. This is the case the fleet actually hits.
func TestCameraIsReacquiredAfterAPowerCycle(t *testing.T) {
	var present atomic.Bool
	present.Store(true)
	// A fresh body each time, as a real replug produces.
	var opens atomic.Int32
	c := New("test-id", "Fake", func() (ptp.Camera, error) {
		if !present.Load() {
			return nil, errors.New("no camera")
		}
		opens.Add(1)
		return &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}, nil
	})
	c.AliveFn = present.Load
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Open(ctx)
	waitConnected(t, c)

	present.Store(false)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && c.Connected() {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Connected() {
		t.Fatal("not released after power-off")
	}

	present.Store(true) // powered back on
	waitConnected(t, c)
	if opens.Load() < 2 {
		t.Errorf("the camera was opened %d times, want a genuine re-acquisition", opens.Load())
	}
}

// A single missed probe is not a disconnection — USB enumeration can blink
// during a bus reset, and tearing the session down for one blink would drop a
// camera that never went anywhere.
func TestSingleMissedProbeDoesNotTearDown(t *testing.T) {
	f := &fakeBody{}
	var misses atomic.Int32
	c := New("test-id", "Fake", func() (ptp.Camera, error) { return f, nil })
	c.AliveFn = func() bool {
		// Absent exactly once, then present again.
		return misses.Add(1) != 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Open(ctx)
	waitConnected(t, c)

	time.Sleep(probeInterval * 3)
	if !c.Connected() {
		t.Error("one missed probe tore the session down; the debounce is not working")
	}
}

// A camera that is still enumerated but has stopped answering cannot be seen by
// the presence probe. ErrNotResponding is the only signal, and it must cause a
// re-acquisition.
func TestWedgedSessionTriggersReacquire(t *testing.T) {
	c := New("test-id", "Fake", func() (ptp.Camera, error) {
		return &fakeBody{frame: testJPEG(t, 16, 16), filename: "x.jpg"}, nil
	})
	c.AliveFn = func() bool { return true } // still on the bus throughout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Open(ctx)
	waitConnected(t, c)

	c.noteTransportError(fmt.Errorf("capture: %w", ptp.ErrNotResponding))

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !c.needsReconnect.Load() == false {
		time.Sleep(10 * time.Millisecond)
	}
	// The supervisor consumes the flag, tears down and re-acquires; the
	// observable end state is a live camera again.
	waitConnected(t, c)
}

// A refusal is NOT a disconnection. A camera saying "not now" — a dial owns the
// setting, it is still writing to card — is alive and answering, and tearing the
// session down would turn a recoverable refusal into a reconnect storm.
func TestRefusalDoesNotTriggerReconnect(t *testing.T) {
	c := New("test-id", "Fake", func() (ptp.Camera, error) { return &fakeBody{}, nil })
	c.noteTransportError(errors.New("device busy: the camera is still writing to card"))
	if c.needsReconnect.Load() {
		t.Error("an ordinary refusal was treated as a dead session")
	}
	c.noteTransportError(ptp.ErrStalled)
	if c.needsReconnect.Load() {
		t.Error("a stalled pipe (an unsupported operation) was treated as a dead session")
	}
}

// Every frame the camera is holding must be taken, not just the newest. A
// Fujifilm body counts a frame as undownloaded until it is DELETED, so one left
// behind stays in a volatile store that never empties, and the shutter
// eventually answers RefusedRightNow.
func TestCollectFrameTakesOneAndDeletesAll(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "DSCF0009.JPG"}
	f.handles = []uint32{0x41, 0x42, 0x43}
	c := openFake(t, f)

	data, name, err := c.collectFrame(false)
	if err != nil {
		t.Fatalf("fetchFrame: %v", err)
	}
	if name != "DSCF0009.JPG" || len(data) == 0 {
		t.Errorf("returned %q with %d bytes", name, len(data))
	}
	// One exposure, one transfer: Alpaca has nowhere to put a second image.
	if f.downloads != 1 {
		t.Errorf("%d frames crossed the wire, want 1 — ImageBytes carries one image", f.downloads)
	}
	// But every frame must leave the camera, or the body stays stuck.
	if len(f.deleted) != 3 {
		t.Fatalf("deleted %v, want all three frames removed from the camera", f.deleted)
	}
	for i, want := range []uint32{0x41, 0x42, 0x43} {
		if f.deleted[i] != want {
			t.Errorf("deleted[%d] = %#x, want %#x", i, f.deleted[i], want)
		}
	}
}

// A delete that fails must not be swallowed: it is the leading edge of a store
// that fills, and the frame itself is still a real photograph.
func TestCollectFrameReportsAFailedDelete(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "DSCF0010.JPG"}
	f.handles = []uint32{0x51}
	f.delErr = errors.New("camera refused")
	c := openFake(t, f)

	data, _, err := c.collectFrame(false)
	if err != nil {
		t.Fatalf("a failed delete lost the frame: %v", err)
	}
	if len(data) == 0 {
		t.Error("the frame was discarded because the delete failed")
	}
	if len(f.deleted) != 1 {
		t.Errorf("the delete was not attempted: %v", f.deleted)
	}
}

// A frame that will not download must be LEFT on the camera. Deleting it would
// destroy the only copy on a body whose card write is off.
func TestCollectFrameKeepsAFrameItCannotDownload(t *testing.T) {
	f := &failingDownload{fakeBody: fakeBody{filename: "DSCF0011.RAF"}}
	f.handles = []uint32{0x61}
	c := openFake(t, &f.fakeBody)
	c.download = f

	if _, _, err := c.collectFrame(false); err == nil {
		t.Fatal("a failed download was reported as success")
	}
	if len(f.deleted) != 0 {
		t.Errorf("a frame that would not download was deleted anyway: %v", f.deleted)
	}
}

type failingDownload struct{ fakeBody }

func (f *failingDownload) Download(uint32) ([]byte, string, error) {
	return nil, "", errors.New("transfer failed")
}

// The card-only path: capture -> skip -> delete. Nothing crosses USB, but the
// delete still happens, because a frame left in the buffer is what wedges the
// camera — moving the bytes is the optional part, not the removal.
func TestCollectFrameSkipStillDeletes(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "DSCF0012.RAF"}
	f.handles = []uint32{0x71}
	c := openFake(t, f)

	data, _, err := c.collectFrame(true)
	if err != nil {
		t.Fatalf("collectFrame(skip): %v", err)
	}
	if data != nil {
		t.Errorf("%d bytes crossed the wire in card-only mode, want none", len(data))
	}
	if f.downloads != 0 {
		t.Errorf("%d downloads in card-only mode, want 0", f.downloads)
	}
	if len(f.deleted) != 1 || f.deleted[0] != 0x71 {
		t.Errorf("deleted %v, want the frame removed even though it was not fetched", f.deleted)
	}
}

// In card-only mode an exposure completes and is timed, but there is no image
// to serve: the photograph is on the card.
func TestCardOnlyExposureServesNoImage(t *testing.T) {
	f := &fakeBody{frame: testJPEG(t, 64, 48), filename: "DSCF0013.RAF"}
	c := openFake(t, f)
	c.CardOnly = true

	if err := c.StartExposure(0.01, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && c.CameraState() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if c.ImageReady() {
		t.Error("ImageReady is true in card-only mode, but nothing was transferred")
	}
	if f.downloads != 0 {
		t.Errorf("%d downloads in card-only mode, want 0", f.downloads)
	}
	if len(f.deleted) == 0 {
		t.Error("the frame was left on the camera, which wedges the body")
	}
	if d, err := c.LastExposureDuration(); err != nil || d <= 0 {
		t.Errorf("LastExposureDuration = %v, %v; the exposure still happened", d, err)
	}
}
