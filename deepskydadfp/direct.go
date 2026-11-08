package driver

import (
	"fmt"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Status is a freshly queried panel snapshot. Brightness is the configured level, even while the light is off.
type Status struct {
	Model      string `json:"model"`
	Firmware   string `json:"firmware"`
	HasCover   bool   `json:"hasCover"`
	HasLight   bool   `json:"hasLight"`
	Cover      string `json:"cover"`
	LightOn    bool   `json:"lightOn"`
	Brightness int    `json:"brightness"`
}

// ReadStatus obtains a fresh, complete hardware snapshot. Failed reads release
// the link; the caller decides whether to start a new session.
func (p *Panel) ReadStatus() (Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.port == nil {
		return Status{}, fmt.Errorf("panel is not connected")
	}
	if err := p.refreshLocked(); err != nil {
		p.dropLocked()
		return Status{}, err
	}
	model := map[int]string{1: "FP2", 2: "FT2", 3: "OFP2", 4: "FP2+", 5: "SFP2"}[p.product]
	cover := "unknown"
	switch p.state.cover {
	case alpacadev.CoverNotPresent:
		cover = "not_present"
	case alpacadev.CoverClosed:
		cover = "closed"
	case alpacadev.CoverMoving:
		cover = "moving"
	case alpacadev.CoverOpen:
		cover = "open"
	}
	return Status{Model: model, Firmware: p.firmware, HasCover: p.product != 2, HasLight: p.product != 5, Cover: cover, LightOn: p.state.on, Brightness: p.state.brightness}, nil
}
