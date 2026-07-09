package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// init registers this driver in the goalpaca driver registry, so a composed
// host (alpacahurd) can construct it from a config entry by importing this
// package.
func init() {
	registry.Register(registry.Driver{
		Name:          "focuslynx",
		Type:          alpacadev.FocuserType,
		Description:   "Optec FocusLynx/ThirdLynx focuser hub",
		ConfigExample: `{ "driver": "focuslynx", "index": 0, "channel": 1 }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Index    int    `json:"index,omitempty"`
				Nickname string `json:"nickname,omitempty"` // stable protocol nickname; resolves hub+channel at connect
				Channel  int    `json:"channel,omitempty"`  // hub channel (1 or 2)
			}
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
