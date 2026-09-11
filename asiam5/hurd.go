package driver

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/lx200/am5"
)

// Config contains device selection and settings.
type Config struct {
	Serial string `json:"serial,omitempty" alpaca:"label=USB serial,when=start,help=Optional USB serial; leave serial and addr empty to discover an attached AM-series mount. Set serial to select one when several are attached"`
	Addr   string `json:"addr,omitempty" alpaca:"label=Host or IP,when=start,help=The WiFi address of the mount. In access-point mode this is 192.168.4.1; on a home network it is whatever the router gave it"`
	Port   int    `json:"tcpPort,omitempty" alpaca:"label=Port,min=1,max=65535,when=start,help=TCP port the mount listens on; empty uses 4030 which is the only port the ZWO firmware serves. Named tcpPort because port is reserved for the Alpaca server itself"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "asiam5",
		Type:          alpacadev.TelescopeType,
		Description:   "ZWO AM-series harmonic mount (USB serial or TCP)",
		ConfigExample: `{ "driver": "asiam5", "serial": "0123456789ABCDEF" }`,
		Config:        func() any { return &Config{} },
		// Either key reaches the mount, and they are different transports rather than fallbacks:
		// the USB serial binds the cable, the address binds the WiFi. Serial leads because it is
		// what a scan can fill — the network side answers nothing until it is dialled, so an
		// address is always typed.
		Identity: []string{"serial", "addr"},
		Scan:     scanMounts,
		// 'G': the AM series track as German-style equatorials ('A' for alt-az installs).
		FrontEnd: lx200FrontEnd('G', "ZWO AM"),
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			conn := cfg.Addr
			if conn == "" {
				conn = cfg.Serial
			}
			if conn == "" {
				conn = "auto"
			}
			d := NewTelescope(cfg.Serial, cfg.Addr, cfg.Port)
			d.ID = "zwoam5-" + conn
			d.DevName = "ZWO AM5"
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			d.Desc = "ZWO AM-series mount (" + conn + ")"
			if cfg.Serial == "" && cfg.Addr == "" {
				d.Desc = "ZWO AM-series mount (automatic USB discovery)"
			}
			return d, nil
		},
	})
}

// scanMounts lists the AM-series mounts this machine can reach, over either transport.
//
// USB first, because it identifies itself: ZWO's own VID:PID narrows the ports and :GVP# confirms
// a mount rather than one of their cameras, so a row costs one open and fills the serial that
// pins it — the value nobody can read off the case.
//
// Then the network, because the mount advertises nothing at all. It answers no mDNS and no
// broadcast, and it will not report the address DHCP gave it, so the only way to find one on a
// home network is to look. A failed sweep is not a failed scan: a machine with no usable subnet,
// or one whose network refuses the attempt, still has its USB mounts to report.
func scanMounts(ctx context.Context) ([]registry.Found, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found, err := am5.Discover()
	if err != nil {
		return nil, err
	}
	if lan, nerr := am5.DiscoverNetwork(ctx, 0); nerr == nil {
		found = append(found, lan...)
	}
	out := make([]registry.Found, 0, len(found))
	for _, d := range found {
		product := d.Product
		if product == "" {
			product = "ZWO AM"
		}
		vals := map[string]any{}
		var label string
		switch {
		case d.Addr != "":
			host, port, err := net.SplitHostPort(d.Addr)
			if err != nil {
				host = d.Addr
			}
			// The host goes in the entry, not "host:port": the port is its own field, and pasting
			// a joined string into one labelled Host is not how anyone expects to configure it.
			vals["addr"] = host
			if p, err := strconv.Atoi(port); err == nil && p != am5.DefaultTCPPort {
				vals["tcpPort"] = p
			}
			label = fmt.Sprintf("%s at %s (WiFi)", product, d.Addr)
		default:
			label = fmt.Sprintf("%s on %s", product, d.Port)
			if d.Serial != "" {
				vals["serial"] = d.Serial
				label = fmt.Sprintf("%s (serial %s)", label, d.Serial)
			}
		}
		if d.Version != "" {
			label = fmt.Sprintf("%s (firmware %s)", label, d.Version)
		}
		out = append(out, registry.Found{Label: label, Values: vals})
	}
	return out, nil
}
