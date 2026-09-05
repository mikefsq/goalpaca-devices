// Command roiprobe captures hardware frames for ROI diagnostics.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	driver "github.com/mikefsq/goalpaca-devices/astrocam"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	x := flag.Int("x", 4678, "ROI start x")
	y := flag.Int("y", 2960, "ROI start y")
	w := flag.Int("w", 1840, "ROI width")
	h := flag.Int("h", 1146, "ROI height")
	exp := flag.Float64("exp", 0.01, "exposure seconds")
	n := flag.Int("n", 6, "exposures")
	client := flag.Bool("client", false, "re-send geometry, gain and offset before every exposure, as a client does")
	gain := flag.Int("gain", 100, "gain to re-send with -client")
	offset := flag.Int("offset", 50, "offset to re-send with -client")
	out := flag.String("out", "", "write the driver ImageFrame pixels to this .ser")
	wireOut := flag.String("wireout", "", "write the Alpaca ImageBytes pixels to this .ser")
	flag.Parse()

	dev := driver.NewPureASICamera(0, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := dev.Open(ctx); err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = dev.Close(context.Background()) }()
	for i := 0; !dev.Connected(); i++ {
		if i > 100 {
			log.Fatal("camera never connected")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The client's order, as goastro issues it: bin, then origin to zero, size, origin.
	_ = dev.SetBinX(1)
	_ = dev.SetBinY(1)
	_ = dev.SetStartX(0)
	_ = dev.SetStartY(0)
	_ = dev.SetNumX(*w)
	_ = dev.SetNumY(*h)
	_ = dev.SetStartX(*x)
	_ = dev.SetStartY(*y)
	if _, err := dev.Action("videomode", "on"); err != nil {
		log.Fatalf("videomode on: %v", err)
	}

	// Two recordings of the same frames, one from each layer: what the driver hands a host
	// in-process, and what the Alpaca wire delivers to a client. Comparing them against each
	// other, and each against a single shot, says which layer a shear belongs to.
	var drv, wire *serWriter
	var base string
	// The Bayer phase the frame actually carries, so a viewer debayers a colour camera's file
	// the way the sensor is laid out instead of showing a mono mosaic. The ROI origin shifts the
	// phase, which is why it comes from the driver's BayerOffset rather than from the sensor type
	// alone.
	colorID := serColorID(dev)
	if *out != "" {
		var err error
		if drv, err = newSER(*out, dev.NumX(), dev.NumY(), 16, colorID); err != nil {
			log.Fatalf("driver ser: %v", err)
		}
	}
	if *wireOut != "" {
		srv := alpacadev.New(alpacadev.Config{
			Discovery:    alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
			ServerName:   "roiprobe",
			Manufacturer: "test",
		})
		if err := srv.Register(alpacadev.CameraType, 0, dev); err != nil {
			log.Fatalf("register: %v", err)
		}
		ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
		defer ts.Close()
		base = ts.URL + "/api/v1/camera/0/"
		var err error
		if wire, err = newSER(*wireOut, dev.NumX(), dev.NumY(), 16, colorID); err != nil {
			log.Fatalf("wire ser: %v", err)
		}
	}
	// Timed from the FIRST frame, not from the start: opening and initialising the camera varies
	// by seconds between runs, which swamps a per-frame figure and makes two runs incomparable.
	var loopStart time.Time
	for i := 0; i < *n; i++ {
		if *client {
			// What an ASCOM client re-sends before every exposure: the whole geometry, then the
			// gain and offset. All of it lands on a stream that is already running.
			_ = dev.SetBinX(1) // a client re-sends the bin too, and that is the call that reprograms
			_ = dev.SetStartX(0)
			_ = dev.SetStartY(0)
			_ = dev.SetNumX(*w)
			_ = dev.SetNumY(*h)
			_ = dev.SetStartX(*x)
			_ = dev.SetStartY(*y)
			_ = dev.SetGain(*gain)
			_ = dev.SetOffset(*offset)
		}
		if err := dev.StartExposure(*exp, true); err != nil {
			log.Fatalf("exposure %d: %v", i, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for !dev.ImageReady() {
			if time.Now().After(deadline) {
				log.Fatalf("exposure %d never became ready", i)
			}
			time.Sleep(2 * time.Millisecond)
		}
		fr, err := dev.ImageFrame()
		if err != nil {
			log.Fatalf("frame %d: %v", i, err)
		}
		if drv != nil {
			if err := drv.write(fr.Pixels); err != nil {
				log.Fatalf("write driver frame %d: %v", i, err)
			}
		}
		// The same exposure over Alpaca, in the binary form the client asks for. Fetched after
		// the driver copy so both describe ONE frame: a difference between the two files is the
		// wire layer's, not two different moments of sky.
		if wire != nil {
			pix, ww, hh, err := fetchImageBytes(base)
			if err != nil {
				log.Fatalf("imagebytes frame %d: %v", i, err)
			}
			if ww != fr.Width || hh != fr.Height {
				log.Fatalf("imagebytes frame %d is %dx%d, the driver says %dx%d", i, ww, hh, fr.Width, fr.Height)
			}
			if err := wire.write(pix); err != nil {
				log.Fatalf("write wire frame %d: %v", i, err)
			}
		}
		if i == 0 {
			loopStart = time.Now()
		}
		head := fr.Pixels[:4]
		fmt.Printf("frame %d: %dx%d %d bytes  head %02x%02x%02x%02x\n",
			i, fr.Width, fr.Height, len(fr.Pixels), head[0], head[1], head[2], head[3])
	}
	if el := time.Since(loopStart); *n > 1 && el > 0 {
		fmt.Printf("LOOP %d frames in %.3fs = %.1f fps (%.2f ms/frame)\n",
			*n-1, el.Seconds(), float64(*n-1)/el.Seconds(), 1000*el.Seconds()/float64(*n-1))
	}
	_, _ = dev.Action("videomode", "off")
	if drv != nil {
		_ = drv.close()
	}
	if wire != nil {
		_ = wire.close()
	}
}
