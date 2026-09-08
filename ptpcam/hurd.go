package ptpcam

import (
	"context"
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

// Config contains device selection and settings.
type Config struct {
	Vendor       string  `json:"vendor,omitempty"       alpaca:"label=Vendor,when=start,help=fuji or sony"`
	Serial       string  `json:"serial,omitempty"       alpaca:"label=Serial,when=start,help=Camera body serial"`
	SensorWidth  int     `json:"sensorWidth,omitempty"  alpaca:"label=Sensor width (px),min=0,when=start"`
	SensorHeight int     `json:"sensorHeight,omitempty" alpaca:"label=Sensor height (px),min=0,when=start"`
	PixelSize    float64 `json:"pixelSize,omitempty"    alpaca:"label=Pixel size (µm),min=0,when=start"`
}

func init() {
	registry.Register(registry.Driver{
		Name:        "ptpcam",
		Type:        alpacadev.CameraType,
		Description: "Fujifilm/Sony PTP stills camera (no vendor SDK)",
		Config:      func() any { return &Config{} },
		// The body serial is the whole identity: two Fujifilm bodies on one machine are told apart
		// by nothing else, and the USB location changes with the port.
		Identity: []string{"serial"},
		Scan:     scanCameras,
		ConfigExample: `{ "driver": "ptpcam", "vendor": "auto", "pixelSize": 3.04, ` +
			`"name": "X-T5" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
			d.AliveFn = NewAliveProbe(cfg.Vendor, cfg.Serial, d)
			d.HotplugFn = usb.Hotplug
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

// NewAliveProbe checks USB presence without camera traffic.
// A changed attachment marks the session for replacement; cam may be nil.
func NewAliveProbe(vendor, serial string, cam *Camera) func() bool {
	return func() bool {
		devs, err := usb.Enumerate()
		if err != nil {
			// An enumeration failure is not evidence of absence. Reporting the
			// camera gone here would tear down a healthy session because the
			// host's USB registry hiccuped.
			return true
		}
		var open uint64
		if cam != nil {
			open = cam.Attachment()
		}
		present, replaced := attachmentPresent(devs, vendor, serial, open)
		if replaced && cam != nil {
			cam.MarkReplaced()
		}
		return present
	}
}

// attachmentPresent is the probe's judgement over an enumeration: present when
// a body matching vendor and serial is listed under the same attachment as the
// open session (or an attachment neither side can name); replaced when every
// matching body is a different attachment.
func attachmentPresent(devs []usb.DeviceInfo, vendor, serial string, open uint64) (present, replaced bool) {
	for _, d := range devs {
		if serial != "" && d.Serial != serial {
			continue
		}
		if !wantVendor(vendor, ptp.VendorID(d.VID)) {
			continue
		}
		if open != 0 && d.Attachment != 0 && d.Attachment != open {
			replaced = true
			continue
		}
		return true, false
	}
	return false, replaced
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

// scanCameras lists the attached PTP bodies this driver supports.
//
// The serial and the product name are in the USB descriptor, so nothing is opened — which matters
// more here than elsewhere: opening a camera means starting a PTP session, and on macOS the system
// photo daemon competes for exactly that.
//
// Vendor is filled alongside the serial. It is not identity — the serial alone binds the body —
// but a camera the operator has not powered into tethered mode yet is easier to recognise as
// "Fujifilm X-T5" than as a hex serial.
func scanCameras(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := usb.Enumerate()
	if err != nil {
		return nil, err
	}
	var out []registry.Found
	for _, d := range devs {
		var vendor string
		switch ptp.VendorID(d.VID) {
		case ptp.Fujifilm:
			vendor = "fuji"
		case ptp.Sony:
			vendor = "sony"
		default:
			continue // a PTP device this driver does not drive
		}
		vals := map[string]any{"vendor": vendor}
		label := d.Name
		if label == "" {
			label = vendor
		}
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
