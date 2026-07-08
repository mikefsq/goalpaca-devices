// Command asicam exposes ZWO ASI cameras as a standalone ASCOM Alpaca server using
// the goalpaca/server (alpacadev) library and the pure-Go asicam driver — no ZWO
// libASICamera2 SDK. The USB transport is IOKit (macOS) / usbfs (Linux) / WinUSB (Windows).
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mikefsq/astrocam"
	_ "github.com/mikefsq/astrocam/sensors" // registers the PID -> sensor profile table (required by astrocam.Open)
	driver "github.com/mikefsq/goalpaca-devices/astrocam"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11111, "Alpaca HTTP port")
	serial := flag.String("serial", "",
		"comma-separated factory serials (hex) — one Alpaca camera device per serial, in order "+
			"(recommended for multi-camera and start-before-plug). Empty = auto-enumerate all attached.")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// Build the device list. Each entry becomes one Alpaca camera device (number 0,1,…), with its
	// own hardware-management goroutine binding its own camera. Serials give a stable,
	// start-before-plug binding; with none, auto-enumerate and bind each by enumeration index.
	var cams []*driver.PureASICamera
	if s := strings.TrimSpace(*serial); s != "" {
		for i, sn := range strings.Split(s, ",") {
			if sn = strings.TrimSpace(sn); sn != "" {
				cams = append(cams, driver.NewPureASICamera(i, sn))
			}
		}
	} else {
		devs, err := astrocam.Enumerate()
		if err == nil && len(devs) > 0 {
			log.Printf("asicam: %d ASI camera(s) attached", len(devs))
			for i, d := range devs {
				log.Printf("  device %d: %s", i, d)
				cams = append(cams, driver.NewPureASICamera(i, "")) // bind by enumeration index
			}
		}
	}
	if len(cams) == 0 {
		log.Printf("asicam: no cameras to serve yet (pass -serial s1,s2 to advertise devices before plug-in)")
		cams = append(cams, driver.NewPureASICamera(0, "")) // still advertise device 0; it acquires when one appears
	}

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asicam: invalid -discovery %q (want direct|register|off)", *discoveryMode)
	}
	log.Printf("asicam: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asicam",
		Manufacturer:        "mikefsq (ZWO ASI via pure-Go asicam)",
		ManufacturerVersion: "0.1.0",
	})
	for i, cam := range cams {
		if err := srv.Register(alpacadev.CameraType, i, cam); err != nil {
			log.Fatalf("asicam: register camera device %d: %v", i, err)
		}
		log.Printf("asicam: registered camera device %d (%s)", i, cam.ID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asicam: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asicam: %v", err)
	}
	log.Printf("asicam: shut down")
}
