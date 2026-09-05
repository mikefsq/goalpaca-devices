package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
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
