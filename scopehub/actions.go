package driver

import (
	"encoding/json"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
	scopehub "github.com/mikefsq/scope_hub/driver"
)

// Device-specific Actions. Names match case-insensitively.
var actions = []string{
	"SaveSettings",    // write the live settings to flash; they apply at power-on. Returns "ok"
	"RestoreDefaults", // restore default settings in RAM; SaveSettings makes them permanent. Returns "ok"
	"Status",          // every reading as JSON: {"status":...,"dew":...,"aux":...}
	"SwitchLayout",    // how a client can lay out the switches, as JSON; see layout.go
}

func (s *Switch) SupportedActions() []string { return actions }

func (s *Switch) Action(name, params string) (string, error) {
	if !s.Connected() {
		return "", alpacadev.ErrNotConnected
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "savesettings":
		if err := s.b.Do(func(d device) error { return d.Save() }); err != nil {
			return "", err
		}
		return "ok", nil
	case "restoredefaults":
		if err := s.b.Do(func(d device) error { return d.Defaults() }); err != nil {
			return "", err
		}
		return "ok", nil
	case "switchlayout":
		return switchLayoutJSON, nil
	case "status":
		n, err := s.b.Snapshot()
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(struct {
			Status *scopehub.Status `json:"status"`
			Dew    *scopehub.Dew    `json:"dew"`
			Aux    *scopehub.Aux    `json:"aux"`
		}{n.st, n.dew, n.aux})
		return string(out), err
	}
	return "", alpacadev.ErrActionNotImplemented
}
