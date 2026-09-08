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
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start,help=USB bridge serial to bind (stable across replug and port renumbering); empty asks every candidate"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "rst",
		Type:          alpacadev.TelescopeType,
		Description:   "Rainbow Astro RST mount (USB serial, auto-detected)",
		ConfigExample: `{ "driver": "rst" }`,
		Config:        func() any { return &Config{} },
		// The USB bridge serial is the only thing that survives a replug or a port renumbering,
		// and it is the one key this driver binds by.
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
			d := NewTelescope(cfg.Serial)
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
		vals := map[string]any{}
		label := fmt.Sprintf("Rainbow Astro RST on %s", d.Port)
		if d.Version != "" {
			label = fmt.Sprintf("%s (firmware %s)", label, d.Version)
		}
		// A row with no serial is not a lesser row: an entry without one asks every candidate
		// port, which is how this driver has always found a mount, and macOS reports no serial
		// for these bridges anyway.
		if d.Serial != "" {
			vals["serial"] = d.Serial
			label = fmt.Sprintf("%s (bridge serial %s)", label, d.Serial)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
