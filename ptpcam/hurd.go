package ptpcam

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp"
	"github.com/mikefsq/ptp/fuji"
	"github.com/mikefsq/ptp/sony"
	"github.com/mikefsq/ptp/usb"
)

// The vendor packages are imported for their side effects as well as their
// constructors: a vendor registers itself from an init function, and that
// registration is what makes its bodies visible to USB enumeration at all.

// init registers this driver in the goalpaca driver registry, so a composed host
// (alpacahurd) can construct it from a config entry by importing this package.
//
// Sensor geometry and photosite pitch are configuration, not discovery. PTP has
// no property for the sensor's physical dimensions, and some bodies (Sony) do
// not report their pixel dimensions either, so a client cannot compute image
// scale or plate-solve unless the operator supplies "pixelSize" — and, where the
// body is silent, "sensorWidth"/"sensorHeight".
func init() {
	registry.Register(registry.Driver{
		Name:        "ptpcam",
		Type:        alpacadev.CameraType,
		Description: "Fujifilm/Sony PTP stills camera (no vendor SDK)",
		ConfigExample: `{ "driver": "ptpcam", "vendor": "auto", "pixelSize": 3.04, ` +
			`"name": "X-T5" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Vendor       string  `json:"vendor,omitempty"`
				Serial       string  `json:"serial,omitempty"`
				SensorWidth  int     `json:"sensorWidth,omitempty"`
				SensorHeight int     `json:"sensorHeight,omitempty"`
				PixelSize    float64 `json:"pixelSize,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			if !knownVendor(cfg.Vendor) {
				return nil, fmt.Errorf("ptpcam: %q is not a known \"vendor\" "+
					"(want auto, fuji, or sony)", cfg.Vendor)
			}
			// Half a sensor size is not a usable geometry, and silently keeping
			// the camera's own value would hide the typo that caused it.
			if (cfg.SensorWidth == 0) != (cfg.SensorHeight == 0) {
				return nil, errors.New("ptpcam: \"sensorWidth\" and \"sensorHeight\" " +
					"must be given together")
			}
			if cfg.SensorWidth < 0 || cfg.SensorHeight < 0 || cfg.PixelSize < 0 {
				return nil, errors.New("ptpcam: \"sensorWidth\", \"sensorHeight\" and " +
					"\"pixelSize\" must not be negative")
			}

			name := spec.Name
			if name == "" {
				name = "PTP Camera"
			}
			d := New("ptpcam-0", name, NewOpener(cfg.Vendor, cfg.Serial))
			// A liveness probe that sent PTP traffic would compete with an
			// exposure in flight, so presence is read off the USB registry.
			d.AliveFn = NewAliveProbe(cfg.Vendor, cfg.Serial)
			if cfg.SensorWidth > 0 {
				d.SetGeometry(cfg.SensorWidth, cfg.SensorHeight)
			}
			if cfg.PixelSize > 0 {
				d.SetPixelSize(cfg.PixelSize)
			}
			return d, nil
		},
	})
}

// NewOpener returns an opener that binds a body matching vendor ("", "auto",
// "fuji" or "sony") and serial (empty matches any). In auto mode it enumerates
// and takes the first body it has a driver for, which is the common case of one
// camera attached.
func NewOpener(vendor, serial string) func() (ptp.Camera, error) {
	return func() (ptp.Camera, error) {
		switch strings.ToLower(vendor) {
		case "fuji", "fujifilm":
			return fuji.Open(serial)
		case "sony":
			return sony.Open(serial)
		}
		devs, err := usb.Enumerate()
		if err != nil {
			return nil, err
		}
		for _, d := range devs {
			if serial != "" && d.Serial != serial {
				continue
			}
			switch ptp.VendorID(d.VID) {
			case ptp.Fujifilm:
				return fuji.Open(d.Serial)
			case ptp.Sony:
				return sony.Open(d.Serial)
			}
		}
		if serial != "" {
			return nil, fmt.Errorf("no camera with serial %q is attached", serial)
		}
		return nil, errors.New("no supported camera found (is it powered on, and in " +
			"tethered/PC-Remote USB mode?)")
	}
}

// NewAliveProbe reports whether a body matching the filter is still enumerated.
//
// It answers "is the body still on the bus", not "is the session healthy" — a
// camera that has stopped answering is still enumerated, and that case is caught
// by ptp.ErrNotResponding instead.
func NewAliveProbe(vendor, serial string) func() bool {
	return func() bool {
		devs, err := usb.Enumerate()
		if err != nil {
			// An enumeration failure is not evidence of absence. Reporting the
			// camera gone here would tear down a healthy session because the
			// host's USB registry hiccuped.
			return true
		}
		for _, d := range devs {
			if serial != "" && d.Serial != serial {
				continue
			}
			if !wantVendor(vendor, ptp.VendorID(d.VID)) {
				continue
			}
			return true
		}
		return false
	}
}

// knownVendor reports whether v is a vendor filter this build understands. An
// unrecognized value must be rejected rather than quietly treated as auto: a
// typo would otherwise bind whichever body happened to be attached.
func knownVendor(v string) bool {
	switch strings.ToLower(v) {
	case "", "auto", "fuji", "fujifilm", "sony":
		return true
	}
	return false
}

func wantVendor(vendor string, id ptp.VendorID) bool {
	switch strings.ToLower(vendor) {
	case "fuji", "fujifilm":
		return id == ptp.Fujifilm
	case "sony":
		return id == ptp.Sony
	}
	return id == ptp.Fujifilm || id == ptp.Sony
}
