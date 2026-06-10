// Command rst is a standalone ASCOM Alpaca Telescope driver for Rainbow Astro
// RST harmonic mounts (RST-135/300), built directly on the lx200/rst library.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"

	alpacadev "github.com/mikefsq/goalpaca/server"
	driver "github.com/mikefsq/rst-alpaca"
)

func main() {
	port := flag.Int("port", 11202, "Alpaca HTTP port")
	serial := flag.String("serial", "", "USB-serial port; empty = auto-detect the first RST (FTDI 0403:6001)")
	discovery := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	id := *serial
	if id == "" {
		id = "auto"
	}
	tel := driver.NewTelescope(*serial)
	tel.ID = "rst-" + id
	tel.DevName = "Rainbow Astro RST"
	tel.Desc = fmt.Sprintf("Rainbow Astro RST mount (%s)", id)
	tel.Version = "0.1.0"
	tel.Info = "rst — Rainbow Astro RST Alpaca Telescope driver"

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discovery) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("rst: invalid -discovery %q (want direct|register|off)", *discovery)
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "rst",
		Manufacturer:        "mikefsq",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		log.Fatalf("rst: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("rst: serving Alpaca telescope on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("rst: %v", err)
	}
}
