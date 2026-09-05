package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// SER v3 output for inspecting probe frames. Pixel data is little-endian.
const serHeaderSize = 178

const netEpochTicks = 621355968000000000

type serWriter struct {
	f      *os.File
	frames int
}

func newSER(path string, w, h, depth, colorID int) (*serWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, serHeaderSize)
	copy(hdr[0:14], "LUCAM-RECORDER")
	put := func(off, v int) { binary.LittleEndian.PutUint32(hdr[off:], uint32(v)) }
	put(14, 0)       // LuID
	put(18, colorID) // MONO, or the sensor's Bayer phase: the probe writes the frame undebayered
	put(22, 0)       // LittleEndian
	put(26, w)
	put(30, h)
	put(34, depth)
	put(38, 0) // FrameCount, patched on close
	copy(hdr[82:122], "astrocam alpaca driver")
	ticks := netEpochTicks + time.Now().UnixNano()/100
	binary.LittleEndian.PutUint64(hdr[162:], uint64(ticks))
	binary.LittleEndian.PutUint64(hdr[170:], uint64(ticks))
	if _, err := f.Write(hdr); err != nil {
		f.Close()
		return nil, err
	}
	return &serWriter{f: f}, nil
}

func (s *serWriter) write(pix []byte) error {
	if _, err := s.f.Write(pix); err != nil {
		return err
	}
	s.frames++
	return nil
}

func (s *serWriter) close() error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(s.frames))
	if _, err := s.f.WriteAt(b[:], 38); err != nil {
		s.f.Close()
		return err
	}
	if err := s.f.Close(); err != nil {
		return err
	}
	fmt.Printf("wrote %d frames\n", s.frames)
	return nil
}

// SER colour IDs.
const (
	serMono      = 0
	serBayerRGGB = 8
	serBayerGRBG = 9
	serBayerGBRG = 10
	serBayerBGGR = 11
)

// serColorID picks the SER colour ID from what the driver reports: MONO unless the sensor is a
// colour mosaic, and then the phase AT THE FRAME's origin. An odd ROI start flips the phase, so a
// file written with the sensor's own pattern would debayer with the colours swapped.
func serColorID(c interface {
	SensorType() alpacadev.SensorType
	BayerOffsetX() (int, error)
	BayerOffsetY() (int, error)
}) int {
	if c.SensorType() != alpacadev.SensorRGGB {
		return serMono
	}
	bx, _ := c.BayerOffsetX()
	by, _ := c.BayerOffsetY()
	switch {
	case bx&1 == 0 && by&1 == 0:
		return serBayerRGGB
	case bx&1 == 1 && by&1 == 0:
		return serBayerGRBG
	case bx&1 == 0 && by&1 == 1:
		return serBayerGBRG
	default:
		return serBayerBGGR
	}
}
