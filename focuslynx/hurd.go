package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
// Config is the entry's driver-owned keys. Every field selects the hardware
// to bind and applies at the next start; the setup page shows them read-only.
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
