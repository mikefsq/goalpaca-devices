// Command astrofleet runs the enabled goalpaca_devices in one process. Which
// devices are enabled is declared in a JSON config file (see fleet.example.json);
// each enabled device is its own ASCOM Alpaca server on its own port, and acquires
// its hardware whenever it appears, so the fleet can be started on an empty bus.
//
// Discovery is answered once for the whole fleet: the per-device servers keep
// discovery off, and a single responder on UDP 32227 advertises every device port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/mikefsq/astrocam/sensors" // registers the PID -> sensor profile table for the astrocam driver
	alpacadev "github.com/mikefsq/goalpaca/server"
	indiccd "github.com/mikefsq/goindi/ccd"
	indimount "github.com/mikefsq/goindi/mount"
	indiserver "github.com/mikefsq/goindi/server"
	"github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/bridge"
)

// liveMounter is implemented by the mount drivers (their LiveMount returns the
// connected lx200.Mount). It is the seam the LX200 bridge and INDI server consume.
type liveMounter interface {
	LiveMount() (lx200.Mount, error)
}

// liveCamera is implemented by camera drivers that can drive the INDI CCD device
// (their LiveCamera returns the frame source). The seam the INDI hub consumes.
type liveCamera interface {
	LiveCamera() (indiccd.Camera, error)
}

// opticsConfigurable is implemented by mount drivers that accept a shared optics
// holder (so the INDI front-end reports what an Alpaca setoptics Action sets). Any
// driver exposing UseOptics(alpacadev.OpticsStore) is picked up automatically.
type opticsConfigurable interface {
	UseOptics(alpacadev.OpticsStore)
}

// built pairs a configured device spec with the constructed driver, so the extra
// front-ends (INDI hub, LX200 bridge) can be wired onto the same device object.
type built struct {
	spec   DeviceSpec
	dev    alpacadev.Device
	optics *opticsHolder // shared optics holder, when the driver accepts one
}

func main() {
	cfgPath := flag.String("config", "fleet.json", "path to the fleet config JSON file")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("astrofleet: %v", err)
	}

	var logger *log.Logger
	if cfg.logRequests() {
		// One line per Alpaca request (client addr, method, URI, status, duration).
		logger = log.New(os.Stderr, "alpaca ", log.LstdFlags|log.Lmsgprefix)
	}

	c := counters{}
	var servers []*alpacadev.Server
	var ports []int
	var devices []built
	for _, spec := range cfg.Devices {
		if !spec.enabled() {
			log.Printf("astrofleet: skipping %s (disabled)", spec.Driver)
			continue
		}
		if spec.Port == 0 {
			log.Fatalf("astrofleet: device %q: \"port\" is required", spec.Driver)
		}
		srv := alpacadev.New(alpacadev.Config{
			AlpacaPort:          spec.Port,
			Discovery:           alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
			ServerName:          "astrofleet",
			Manufacturer:        "mikefsq",
			ManufacturerVersion: "0.1.0",
			Logger:              logger,
		})
		dev, err := registerDevice(srv, spec, spec.Port, c)
		if err != nil {
			log.Fatalf("astrofleet: device %q: %v", spec.Driver, err)
		}
		b := built{spec: spec, dev: dev}
		// Inject a shared optics holder so the INDI front-end's TELESCOPE_INFO reports
		// whatever an Alpaca setoptics Action sets — one source of truth, both fronts.
		if oc, ok := dev.(opticsConfigurable); ok {
			b.optics = newOpticsHolder(spec.Aperture, spec.ApertureArea, spec.FocalLength,
				spec.GuiderAperture, spec.GuiderFocalLength)
			oc.UseOptics(b.optics)
		}
		servers = append(servers, srv)
		ports = append(ports, spec.Port)
		devices = append(devices, b)
		log.Printf("astrofleet: %s on :%d", spec.Driver, spec.Port)
	}
	if len(servers) == 0 {
		log.Fatalf("astrofleet: no enabled devices in %s", *cfgPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !strings.EqualFold(cfg.Discovery, "off") {
		if err := runDiscovery(ctx, ports, cfg.IPv6); err != nil {
			log.Fatalf("astrofleet: discovery: %v", err)
		}
		log.Printf("astrofleet: discovery responder on :%d for %d port(s)", discoveryPort, len(ports))
	}

	startINDI(ctx, cfg, devices)
	startBridges(ctx, cfg, devices)

	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *alpacadev.Server) { errc <- s.Run(ctx) }(s)
	}
	log.Printf("astrofleet: serving %d device(s) (Ctrl-C to stop)", len(servers))

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			log.Fatalf("astrofleet: %v", err)
		}
	}
	log.Printf("astrofleet: shut down")
}

// startINDI hosts a single in-process INDI server on one port (default 7624) with
// every INDI-capable device (currently mounts), multiplexed by device name. Each
// device drives the same mount object the Alpaca server does — it is a sibling
// front-end, not a layer over it. INDI has no discovery, so the device names (which
// clients select by) must be unique; a collision is a startup error.
func startINDI(ctx context.Context, cfg *Config, devices []built) {
	if !cfg.Indi.Enable {
		return
	}
	hub := indiserver.New(fmt.Sprintf(":%d", cfg.Indi.port()), indiserver.WithLogger(log.Printf))
	added := 0
	for _, b := range devices {
		if !b.spec.indiEnabled() {
			continue
		}
		name := indiName(b.spec)
		var dev indiserver.Device
		switch {
		case isLiveMounter(b.dev):
			var opts []indimount.Option
			if b.optics != nil {
				opts = append(opts, indimount.WithOptics(b.optics))
			}
			rate := b.spec.GuideRate
			if rate == 0 {
				rate = 0.5
			}
			opts = append(opts, indimount.WithGuideRate(rate))
			dev = indimount.New(name, b.dev.(liveMounter).LiveMount, opts...)
		case isLiveCamera(b.dev):
			dev = indiccd.New(name, b.dev.(liveCamera).LiveCamera)
		default:
			continue // not an INDI-capable device
		}
		if err := hub.AddDevice(dev); err != nil {
			log.Fatalf("astrofleet: indi: %v", err)
		}
		added++
	}
	if added == 0 {
		return
	}
	go func() {
		log.Printf("astrofleet: INDI server on :%d for %d device(s)", cfg.Indi.port(), added)
		if err := hub.Serve(ctx); err != nil && ctx.Err() == nil {
			log.Printf("astrofleet: indi: %v", err)
		}
	}()
}

// startBridges serves a Meade-LX200 TCP server (Stellarium/SkySafari) per mount.
// Each mount needs its own port (the LX200 protocol can't multiplex), so when the
// fleet-level "lx200" block is enabled every mount gets one assigned from BasePort
// upward; a mount can pin its own with "lx200Port", which also enables it on its own.
func startBridges(ctx context.Context, cfg *Config, devices []built) {
	next := cfg.LX200.basePort()
	for _, b := range devices {
		lm, ok := b.dev.(liveMounter)
		if !ok {
			if b.spec.LX200Port != 0 {
				log.Fatalf("astrofleet: %q sets \"lx200Port\" but is not a mount", b.spec.Driver)
			}
			continue
		}
		port := b.spec.LX200Port // explicit per-mount override
		if port == 0 {
			if !cfg.LX200.Enable {
				continue
			}
			port = next
			next++
		}
		srv := bridge.New(fmt.Sprintf(":%d", port), lm.LiveMount, bridge.WithLogger(log.Printf))
		p := port
		go func() {
			log.Printf("astrofleet: LX200 bridge on :%d for %s", p, b.spec.Driver)
			if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
				log.Printf("astrofleet: lx200 bridge: %v", err)
			}
		}()
	}
}

func isLiveMounter(d alpacadev.Device) bool { _, ok := d.(liveMounter); return ok }
func isLiveCamera(d alpacadev.Device) bool  { _, ok := d.(liveCamera); return ok }

// indiName is the INDI device id clients select by: the configured name, or a
// stable fallback derived from the driver and its binding identity.
func indiName(spec DeviceSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	id := pick(spec.Serial, spec.Addr, spec.Nickname)
	if id == "" {
		id = strconv.Itoa(spec.Index)
	}
	return spec.Driver + "-" + id
}
