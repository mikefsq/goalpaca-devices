package driver

import (
	"context"
	"fmt"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/optec/focuslynx"
)

// Config contains device selection and settings.
type Config struct {
	Index    int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start,help=Bind the Nth attached unit; prefer Serial where the device has one"`
	Nickname string `json:"nickname,omitempty" alpaca:"label=Nickname,when=start,help=Protocol nickname; resolves hub and channel at connect"`
	Channel  int    `json:"channel,omitempty" alpaca:"label=Channel,min=1,max=2,when=start,help=Hub channel"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "focuslynx",
		Type:          alpacadev.FocuserType,
		Description:   "Optec FocusLynx/ThirdLynx focuser hub",
		ConfigExample: `{ "driver": "focuslynx", "index": 0, "channel": 1 }`,
		Config:        func() any { return &Config{} },
		// A hub's nickname is what survives a replug — the enumerator reports no serial for these
		// — and the channel picks the focuser on it.
		Identity: []string{"nickname", "index"},
		Scan:     scanHubs,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			// Prefer the stable protocol-nickname binding when given (channel is then
			// discovered over the protocol); otherwise bind by enumeration index + channel.
			var d *OptecFocuser
			if cfg.Nickname != "" {
				d = NewOptecFocuserByNickname(cfg.Index, cfg.Nickname)
			} else {
				ch := cfg.Channel
				if ch == 0 {
					ch = 1
				}
				d = NewOptecFocuser(cfg.Index, ch)
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}

// scanHubs lists the serial ports a FocusLynx or ThirdLynx hub answers on.
//
// The enumerator reports no serial for these, and the protocol nickname that identifies a hub can
// only be read by talking to it — which a scan does not do. So a row names its port and its baud
// rate, and the operator pins a nickname afterwards if they want one.
func scanHubs(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	devs, err := focuslynx.Enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]registry.Found, 0, len(devs))
	for i, d := range devs {
		out = append(out, registry.Found{
			Label:  fmt.Sprintf("Lynx hub on %s (%d baud)", d.Port, d.Baud),
			Values: map[string]any{"index": i},
		})
	}
	return out, nil
}
