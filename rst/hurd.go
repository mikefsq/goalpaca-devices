package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/rst"
)

// Config contains device selection and settings.
type Config struct {
	Serial      string `json:"serial,omitempty"      alpaca:"label=USB bridge serial,when=start,help=Serial of the USB adapter the mount is on; narrows the search before any port is opened"`
	MountSerial string `json:"mountSerial,omitempty" alpaca:"label=Mount serial,when=start,help=The serial of the mount itself (:AS#) as printed on the unit. Recorded for identification; when set it is checked after connecting and a different mount is refused"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "rst",
		Type:          alpacadev.TelescopeType,
		Description:   "Rainbow Astro RST mount (USB serial, auto-detected)",
		ConfigExample: `{ "driver": "rst" }`,
		Config:        func() any { return &Config{} },
		// The BRIDGE serial is the identity, and only it: the port enumerator reports it, so a
		// host binds the mount without opening a single port. The mount's own serial can only be
		// had by opening a port and asking, which is the USB scan this key exists to avoid — so it
		// is reported, shown and verified, but never searched on.
		Identity: []string{"serial"},
		Scan:     scanMounts,
		// 'G': the RST rides its RA/DEC axes as a German-style equatorial.
		FrontEnd: lx200FrontEnd('G', "RainbowAstro"),
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			id := cfg.Serial
			if id == "" {
				id = "auto"
			}
			d := NewTelescope(cfg.Serial, cfg.MountSerial)
			d.ID = "rst-" + id
			d.DevName = "Rainbow Astro RST"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "Rainbow Astro RST mount (" + id + ")"
			return d, nil
		},
	})
}

// scanMounts lists the RST mounts attached to this machine.
//
// rst.Discover asks each candidate FTDI port for its firmware version and closes it again, which
// is the only way to tell a mount from anything else on the same bridge chip: 0403:6001 is what a
// Unihedron SQM and an MGPBox use too, so listing ports by descriptor would report every
// instrument on the rig as a mount.
//
// The USB bridge serial is what gets stored, and macOS does not report one — those rows carry the
// port and the firmware version instead, and the operator binds a mount that this driver will find
// again by asking, as it always has.
func scanMounts(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found, err := rst.Discover()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(found))
	for _, d := range found {
		// Both serials are reported, for different jobs. The bridge serial BINDS — it comes from
		// the port enumerator, so a host reaches the mount without opening anything. The mount
		// serial IDENTIFIES: it is what an operator recognises, and what proves the thing that
		// answered is the mount they configured rather than another one on the same adapter.
		vals := map[string]any{}
		label := fmt.Sprintf("Rainbow Astro RST on %s", d.Port)
		if d.MountSerial != "" {
			// Leads the label: it is the number printed on the unit, and the one an operator
			// choosing between two mounts recognises.
			label = fmt.Sprintf("Rainbow Astro RST %s on %s", d.MountSerial, d.Port)
			vals["mountSerial"] = d.MountSerial
		}
		if d.Version != "" {
			label = fmt.Sprintf("%s (firmware %s)", label, d.Version)
		}
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (bridge serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
