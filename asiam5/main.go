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
)

func main() {
	port := flag.Int("port", 11201, "Alpaca HTTP port")
	serial := flag.String("serial", "", "USB-serial port (e.g. /dev/tty.usbserial-XXXX)")
	addr := flag.String("addr", "", "WiFi/TCP address host:port (e.g. 192.168.4.1:4030)")
	discovery := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	if *serial == "" && *addr == "" {
		log.Fatalf("asiam5: set -serial PORT or -addr HOST:PORT")
	}

	conn := *addr
	if conn == "" {
		conn = *serial
	}
	tel := NewTelescope(*serial, *addr)
	tel.ID = "zwoam5-" + conn
	tel.DevName = "ZWO AM5"
	tel.Desc = fmt.Sprintf("ZWO AM-series mount (%s)", conn)
	tel.Version = "0.1.0"
	tel.Info = "asiam5 — ZWO AM-series Alpaca Telescope driver"

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discovery) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asiam5: invalid -discovery %q (want direct|register|off)", *discovery)
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asiam5",
		Manufacturer:        "mikefsq",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		log.Fatalf("asiam5: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asiam5: serving Alpaca telescope on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asiam5: %v", err)
	}
}
