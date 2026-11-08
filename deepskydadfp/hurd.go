// Package driver serves Deep Sky Dad FP2-family panels as Alpaca CoverCalibrators.
package driver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

type Config struct {
	Serial string `json:"serial,omitempty" alpaca:"label=USB serial,when=start,help=Stable USB serial from discovery; preferred over a device path"`
	Path   string `json:"path,omitempty" alpaca:"label=Serial device path,when=start,help=Optional /dev/serial/by-id/... or COM port; overrides enumeration and always uses serial"`
}

func init() {
	drv := registry.Driver{
		Name: "deepskydadfp", Type: alpacadev.CoverCalibratorType,
		Description: "Deep Sky Dad FP2-family flat panel and motorized cover",
		Config:      func() any { return &Config{} }, Identity: []string{"serial", "path"}, Scan: scanPanels,
		ConfigExample: `{"driver":"deepskydadfp","serial":"YOUR-USB-SERIAL"}`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			p := NewPanel(cfg)
			p.Instance = spec.Instance
			if spec.Name != "" {
				p.DevName = spec.Name
			}
			return p, nil
		},
	}
	registry.Register(drv)
}

// portInfo contains the USB descriptor fields used to identify a panel without
// opening its serial port.
type portInfo struct {
	Name         string
	IsUSB        bool
	VID, PID     string
	Product      string
	SerialNumber string
}

// candidate matches by USB product name. Windows can report only a generic name such as
// "USB Serial Device (COM5)", so a generic or empty name also matches the panel's VID and PID.
// Other Pico boards share those IDs, but identify rejects them after the port is opened.
func candidate(p *portInfo) bool {
	if !p.IsUSB {
		return false
	}
	if strings.Contains(strings.ToLower(p.Product), "deep sky dad fp2") {
		return true
	}
	generic := p.Product == "" || strings.Contains(p.Product, "(COM")
	return generic && strings.EqualFold(p.VID, "2E8A") && strings.EqualFold(p.PID, "000A")
}

// Discovery reads USB descriptors only, including panels already held by another
// application. It does not open ports or probe unrelated serial instruments.
func scanPanels(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ports, err := enumeratePorts()
	if err != nil {
		return nil, err
	}
	out := []registry.Found{}
	for _, p := range ports {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !candidate(p) {
			continue
		}
		values := map[string]any{"path": p.Name}
		if p.SerialNumber != "" {
			values["serial"] = p.SerialNumber
			delete(values, "path")
		}
		out = append(out, registry.Found{Label: fmt.Sprintf("%s (%s)", p.Product, p.Name), Values: values})
	}
	return out, nil
}

func resolvePath(cfg Config) (string, error) {
	if cfg.Path != "" {
		return cfg.Path, nil
	}
	ports, err := enumeratePorts()
	if err != nil {
		return "", err
	}
	var matches []string
	for _, p := range ports {
		if cfg.Serial != "" {
			if p.IsUSB && p.SerialNumber == cfg.Serial {
				matches = append(matches, p.Name)
			}
		} else if candidate(p) {
			matches = append(matches, p.Name)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one matching Deep Sky Dad panel, found %d; select serial or path", len(matches))
	}
	return matches[0], nil
}
