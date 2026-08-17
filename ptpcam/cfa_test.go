package ptpcam

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/fuji"
)

// rawBody is a fake camera that hands back a real RAF and decodes it exactly as
// a Fujifilm body would, so the whole Alpaca path can be exercised without
// hardware.
type rawBody struct {
	fakeBody
	raf []byte
}

func (b *rawBody) Download(uint32) ([]byte, string, error) { return b.raf, "DSCF0001.RAF", nil }
func (b *rawBody) DecodeCFA(raw []byte) (*ptp.CFA, error)  { return fuji.DecodeRAF(raw) }
func (b *rawBody) SensorInfo() (*ptp.CFA, error)           { return (&fuji.Camera{}).SensorInfo() }

var _ ptp.RawDecoder = (*rawBody)(nil)

func sampleRAF(t *testing.T) []byte {
	t.Helper()
	const path = "../../ptp/fuji/testdata/raf/xt5-lossless-lit.raf"
	raf, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s is not present", path)
	}
	return raf
}

// A RAW capture must reach the client as an UNDEMOSAICED 16-bit readout, one
// sample per photosite, at full sensor geometry.
func TestRawCaptureDeliversCFA(t *testing.T) {
	raf := sampleRAF(t)
	f := &rawBody{raf: raf}
	c := openFake(t, &f.fakeBody)
	c.mu.Lock()
	c.download, c.rawdec = f, f
	c.mu.Unlock()

	if err := c.StartExposure(0.01, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	waitIdle(t, c)

	frame, err := c.ImageFrame()
	if err != nil {
		t.Fatalf("ImageFrame: %v", err)
	}
	if frame.Rank != 2 {
		t.Errorf("rank %d, want 2 — a mosaiced readout is one plane, not three", frame.Rank)
	}
	if frame.ElementType != alpaca.ImgUInt16 {
		t.Errorf("element type %v, want ImgUInt16", frame.ElementType)
	}
	if frame.Width != 7872 || frame.Height != 5196 {
		t.Errorf("frame %dx%d, want the FULL 7872x5196 readout including the "+
			"optical-black margin", frame.Width, frame.Height)
	}
	if got, want := len(frame.Pixels), 7872*5196*2; got != want {
		t.Fatalf("%d pixel bytes, want %d", got, want)
	}

	// X-Trans must NOT claim to be Bayer: a client would debayer 6x6 data with
	// a 2x2 kernel and get confidently wrong colour.
	if st := c.SensorType(); st != alpacadev.SensorMonochrome {
		t.Errorf("SensorType %v, want Monochrome for a 6x6 X-Trans mosaic", st)
	}
	if _, err := c.BayerOffsetX(); err == nil {
		t.Error("BayerOffsetX answered for a mosaic ASCOM cannot describe")
	}
	if got := c.MaxADU(); got != 16383 {
		t.Errorf("MaxADU %d, want the sensor's 14-bit white level", got)
	}
}

// The pixels must survive the ImageBytes round trip. Its Rank-2 encoder
// TRANSPOSES to column-major, so a frame that is not transposed back lands
// every sample on the wrong photosite — which for a mosaiced readout means the
// colour of every pixel is wrong.
func TestCFASurvivesImageBytes(t *testing.T) {
	raf := sampleRAF(t)
	want, err := fuji.DecodeRAF(raf)
	if err != nil {
		t.Fatal(err)
	}
	f := &rawBody{raf: raf}
	c := openFake(t, &f.fakeBody)
	c.mu.Lock()
	c.download, c.rawdec = f, f
	c.mu.Unlock()
	c.StartExposure(0.01, true)
	waitIdle(t, c)

	frame, err := c.ImageFrame()
	if err != nil {
		t.Fatal(err)
	}
	wire := alpaca.EncodeImageBytes(frame, 1, 1)
	got, err := alpaca.DecodeImageBytes(wire)
	if err != nil {
		t.Fatalf("DecodeImageBytes: %v", err)
	}
	if got.Width != want.Width || got.Height != want.Height {
		t.Fatalf("round trip gave %dx%d, want %dx%d", got.Width, got.Height, want.Width, want.Height)
	}
	for i := range want.Pixels {
		if v := binary.LittleEndian.Uint16(got.Pixels[i*2:]); v != want.Pixels[i] {
			t.Fatalf("sample %d (%d,%d) came back %d, want %d",
				i, i%want.Width, i/want.Width, v, want.Pixels[i])
		}
	}
}

var _ = time.Second

// The frame must alias the decoded samples rather than copy them: at 40 MP the
// copy was 81.8 MB of allocation and a full memory pass per frame.
func TestFrameAliasesTheSamples(t *testing.T) {
	px := []uint16{0x0201, 0x0403, 0x0605}
	b := samplesAsBytes(px)
	if len(b) != 6 {
		t.Fatalf("%d bytes for 3 samples, want 6", len(b))
	}
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("byte %d is %#x, want %#x — not little-endian wire order", i, b[i], want[i])
		}
	}
	if !nativeLittleEndian {
		t.Skip("big-endian host: the copy path is correct by construction")
	}
	// Aliasing, not copying: writing through one must show in the other.
	px[0] = 0xBEEF
	if b[0] != 0xEF || b[1] != 0xBE {
		t.Error("the byte view did not alias the samples, so a copy is still being made")
	}
}

// Pixel size cannot be discovered over PTP, so it must be configurable — and
// must read as 0 rather than a guess when it is not configured. A client
// computes image scale from it, and a plausible wrong value is worse than an
// obviously absent one.
func TestPixelSizeIsConfiguredNotGuessed(t *testing.T) {
	c := New("x", "x", nil)
	if got := c.PixelSizeX(); got != 0 {
		t.Errorf("PixelSizeX is %v before configuration, want 0", got)
	}
	c.SetPixelSize(3.04)
	if got := c.PixelSizeX(); got != 3.04 {
		t.Errorf("PixelSizeX %v, want 3.04", got)
	}
	if c.PixelSizeY() != c.PixelSizeX() {
		t.Error("the photosites are square; X and Y must agree")
	}
}
