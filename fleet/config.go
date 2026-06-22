package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Config is the fleet configuration: a single ASCOM Alpaca server (one port, one
// discovery responder) hosting exactly the devices listed in Devices. A device is
// "enabled" by appearing in the list; remove it to disable it.
type Config struct {
	Discovery string `json:"discovery"` // direct | off

	// Listen restricts which interfaces the fleet serves on, applied consistently to
	// the Alpaca servers, LX200 bridges, INDI hub, and discovery (which then only
	// advertises where it listens). Each entry is an interface name (e.g. "en0",
	// "eth0", "lo") — expanding to all of its addresses, both IP stacks — or an IP
	// literal (binding just that address; a bare IPv4 literal is IPv4-only). Empty
	// (the default) binds every interface (":port") on both stacks. See resolveListen.
	Listen []string `json:"listen,omitempty"`

	// IPv6 also answers Alpaca discovery over IPv6 multicast (group ff12::a1:9aca),
	// alongside the always-on IPv4 broadcast responder. Defaults to true (the field
	// is a pointer, so an omitted value still means on) since Alpaca clients probe
	// over both; set "ipv6": false to bind IPv4 only. Best-effort: on a host with no
	// usable IPv6 it logs once and IPv4 discovery is unaffected.
	IPv6 *bool `json:"ipv6,omitempty"`

	// Debug enables verbose per-request/response traffic logging for the Alpaca
	// servers (one line per HTTP request) and the INDI hub (per-message traffic).
	// Defaults to false. Lifecycle logs — the "listening" lines and INDI client
	// connect/disconnect — print regardless.
	Debug bool `json:"debug,omitempty"`

	// Indi optionally hosts a single in-process INDI server (one port, devices that
	// opt in via "indi": true, multiplexed by device name) for cross-platform clients
	// like PHD2 and Ekos — no indiserver, no driver binaries. Omit or disable to leave
	// it off.
	Indi IndiConfig `json:"indi,omitempty"`

	// LX200 optionally serves a Meade-LX200 TCP server (Stellarium/SkySafari) for
	// every mount. Unlike INDI's one multiplexed port, LX200 needs one port per mount,
	// so enabling it assigns each mount a port from BasePort upward (a device can pin
	// its own with "lx200Port").
	LX200 LX200Config `json:"lx200,omitempty"`

	Devices []DeviceSpec `json:"devices"`
}

// LX200Config configures the optional per-mount LX200 servers.
type LX200Config struct {
	Enable   bool `json:"enable,omitempty"`
	BasePort int  `json:"basePort,omitempty"` // default 4030 when Enable is set

	// ReadOnlySite makes the bridge ACK a client's site/time set commands without
	// writing them to the mount, so an atlas (SkySafari, Stellarium) can't overwrite a
	// modeled mount's surveyed site/clock and invalidate its pointing model. Reads still
	// report the mount's real values. Off by default (the atlas can sync site/time).
	ReadOnlySite bool `json:"readOnlySite,omitempty"`
}

// basePort returns the LX200 base port, defaulting to 4030.
func (l LX200Config) basePort() int {
	if l.BasePort == 0 {
		return 4030
	}
	return l.BasePort
}

// IndiConfig configures the optional shared INDI server. INDI has no discovery, so
// the port is static (the conventional 7624) and clients are pointed at host:port +
// a device name. A device joins the hub if it is INDI-capable (currently mounts) and
// has not set "indi": false.
type IndiConfig struct {
	Enable bool `json:"enable,omitempty"`
	Port   int  `json:"port,omitempty"` // default 7624 when Enable is set
}

// port returns the INDI port, defaulting to the conventional 7624.
func (i IndiConfig) port() int {
	if i.Port == 0 {
		return 7624
	}
	return i.Port
}


// ipv6Enabled reports whether IPv6 discovery should be answered (default true).
func (c *Config) ipv6Enabled() bool { return c.IPv6 == nil || *c.IPv6 }

// indiEnabled reports whether this device should join the INDI hub. Opt-in: a device
// joins only when it sets "indi": true; the default is Alpaca-only.
func (d DeviceSpec) indiEnabled() bool { return d.Indi != nil && *d.Indi }

// enabled reports whether this device should be registered (default true).
func (d DeviceSpec) enabled() bool { return d.Enable == nil || *d.Enable }

// DeviceSpec declares one enabled device. Driver selects the goalpaca_device; the
// remaining fields bind it to a specific unit. Each declared device is registered
// at startup and gets its own acquire/monitor/re-acquire goroutine, so it is picked
// up whenever its hardware appears — even if nothing is attached when the fleet
// starts — and survives unplug/replug independently of the other devices.
//
// Bind by a stable identity where the driver supports one (serial / TCP addr);
// otherwise the device is selected by enumeration Index (0-based).
type DeviceSpec struct {
	Driver string `json:"driver"` // tenmicron|asiam5|onstep|rst|asicam|asieaf|asiefw|oasisfoc|oasisfw|focuscube|focuslynx
	Name   string `json:"name,omitempty"`

	// Enable toggles this device without removing its entry. Defaults to true
	// (the field is a pointer, so an omitted value still means enabled); set
	// "enable": false to skip it at startup.
	Enable *bool `json:"enable,omitempty"`

	// Port is this device's own Alpaca HTTP port.
	Port int `json:"port,omitempty"`

	// Indi opts a device INTO the shared INDI hub (default out — Alpaca-only). Set
	// "indi": true on a mount (later a camera) to expose it over INDI.
	Indi *bool `json:"indi,omitempty"`

	// LX200Port pins this mount's LX200 server to a specific port, overriding the
	// fleet's auto-assignment. Setting it also enables LX200 for just this mount even
	// when the fleet-level "lx200" block is off.
	LX200Port int `json:"lx200Port,omitempty"`

	Index    int    `json:"index,omitempty"`    // enumeration index (index-bound drivers)
	Serial   string `json:"serial,omitempty"`   // stable USB serial (asicam/asieaf/asiefw/focuscube and serial mounts)
	Nickname string `json:"nickname,omitempty"` // stable protocol nickname (focuslynx; resolves hub+channel at connect)
	Addr     string `json:"addr,omitempty"`     // TCP host:port (tenmicron, networked mounts)

	Channel        int  `json:"channel,omitempty"`        // focuslynx hub channel (1 or 2)
	MaxStep        int  `json:"maxstep,omitempty"`        // focuscube travel (device doesn't report it)
	Unidirectional bool `json:"unidirectional,omitempty"` // filter wheels: always rotate one way

	Aperture     float64 `json:"aperture,omitempty"`     // optics (telescopes): mm (e.g. 130)
	ApertureArea float64 `json:"apertureArea,omitempty"` // optics: m² (default from diameter)
	FocalLength  float64 `json:"focalLength,omitempty"`  // optics: mm (e.g. 1000)

	// Guide-scope optics in mm for INDI TELESCOPE_INFO / GUIDER_*. Omitted defaults to
	// the main scope (the OAG case). A client can also push these at runtime via the
	// mount's setoptics Action.
	GuiderAperture    float64 `json:"guiderAperture,omitempty"`
	GuiderFocalLength float64 `json:"guiderFocalLength,omitempty"`

	// GuideRate is the mount's guide speed as a fraction of sidereal (e.g. 0.5),
	// reported over INDI so PHD2 can scale calibration. Defaults to 0.5 when omitted.
	GuideRate float64 `json:"guideRate,omitempty"`
}

// LoadConfig reads and validates a fleet config file. Unknown JSON fields are
// rejected so a typo in the config is reported rather than silently ignored.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Discovery == "" {
		c.Discovery = "direct"
	}
	return &c, nil
}
