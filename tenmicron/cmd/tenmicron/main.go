// Command tenmicron is a standalone ASCOM Alpaca Telescope driver for 10Micron
// GM-series mounts, built directly on the lx200/tenmicron protocol library.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	alpacadev "github.com/mikefsq/goalpaca/server"
	driver "github.com/mikefsq/tenmicron-alpaca"
)

func main() {
	port := flag.Int("port", 11200, "Alpaca HTTP port")
	addr := flag.String("addr", "", "mount TCP address host:port (e.g. 10.0.1.51:3492)")
	discovery := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	aperture := flag.Float64("aperture", 0, "optics: aperture diameter in metres (ASCOM ApertureDiameter)")
	apertureArea := flag.Float64("aperture-area", 0, "optics: light-collecting area in m² (default: from diameter)")
	focalLength := flag.Float64("focal-length", 0, "optics: focal length in metres (ASCOM FocalLength)")
	flag.Parse()

	if *addr == "" {
		log.Fatalf("tenmicron: -addr is required (mount host:port, e.g. 10.0.1.51:3492)")
	}

	tel := driver.NewTelescope(*addr)
	tel.ID = "10micron-" + *addr
	tel.DevName = "10Micron GM"
	tel.Desc = "10Micron GM-series mount (" + *addr + ")"
	tel.Version = "0.1.0"
	tel.Info = "tenmicron — 10Micron Alpaca Telescope driver"
	tel.SetOptics(*aperture, *apertureArea, *focalLength)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discovery) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("tenmicron: invalid -discovery %q (want direct|register|off)", *discovery)
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "tenmicron",
		Manufacturer:        "mikefsq",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		log.Fatalf("tenmicron: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("tenmicron: serving Alpaca telescope on :%d for mount %s (Ctrl-C to stop)", *port, *addr)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("tenmicron: %v", err)
	}
}
