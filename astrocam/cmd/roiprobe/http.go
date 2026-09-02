package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
)

// The ASCOM ImageBytes metadata header: 44 bytes, all int32 little-endian.
const (
	ibDataStart  = 16
	ibTransmit   = 24
	ibRank       = 28
	ibDimension1 = 32
	ibDimension2 = 36
	ibHeaderSize = 44
)

// fetchImageBytes GETs one exposure over Alpaca in the binary form goastro asks for, and returns
// the frame ROW-major with its geometry.
//
// The transpose is the point of testing this layer separately. ImageBytes is COLUMN-major — the
// wire runs down each column, so element i is (x = i/height, y = i%height) — and getting that
// backwards, or getting the dimensions the wrong way round, produces a frame with the right byte
// count whose content shears. That looks identical to a transport fault and is not one.
func fetchImageBytes(base string) (pix []byte, w, h int, err error) {
	req, _ := http.NewRequest(http.MethodGet, base+"imagearray?ClientID=1&ClientTransactionID=1", nil)
	req.Header.Set("Accept", "application/imagebytes")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(body) < ibHeaderSize {
		return nil, 0, 0, fmt.Errorf("imagebytes reply is %d bytes, shorter than its header", len(body))
	}
	i32 := func(off int) int { return int(int32(binary.LittleEndian.Uint32(body[off:]))) }
	if ct := r.Header.Get("Content-Type"); ct != "application/imagebytes" {
		return nil, 0, 0, fmt.Errorf("server answered %q, not imagebytes", ct)
	}
	start, transmit, rank := i32(ibDataStart), i32(ibTransmit), i32(ibRank)
	w, h = i32(ibDimension1), i32(ibDimension2)
	if rank != 2 {
		return nil, 0, 0, fmt.Errorf("rank %d, want 2", rank)
	}
	// TransmissionElementType 8 = UInt16 (ImgUInt16 in goalpaca), 6 = byte.
	es := 2
	if transmit == 6 {
		es = 1
	}
	data := body[start:]
	if len(data) < w*h*es {
		return nil, 0, 0, fmt.Errorf("imagebytes payload %d bytes, want %d for %dx%d", len(data), w*h*es, w, h)
	}
	out := make([]byte, w*h*2)
	for x := 0; x < w; x++ {
		col := data[x*h*es:]
		for y := 0; y < h; y++ {
			var v uint16
			if es == 2 {
				v = binary.LittleEndian.Uint16(col[y*2:])
			} else {
				v = uint16(col[y])
			}
			binary.LittleEndian.PutUint16(out[(y*w+x)*2:], v)
		}
	}
	return out, w, h, nil
}
