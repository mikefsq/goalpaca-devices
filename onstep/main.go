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
	port := flag.Int("port", 11203, "Alpaca HTTP port")
	serial := flag.String("serial", "", "USB-serial port (e.g. /dev/tty.usbserial-XXXX)")
	addr := flag.String("addr", "", "WiFi/TCP address host:port (e.g. 192.168.0.1:9999)")
	discovery := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	if *serial == "" && *addr == "" {
		log.Fatalf("onstep: set -serial PORT or -addr HOST:PORT")
	}

	conn := *addr
	if conn == "" {
		conn = *serial
	}
	tel := NewTelescope(*serial, *addr)
	tel.ID = "onstep-" + conn
	tel.DevName = "OnStep"
	tel.Desc = fmt.Sprintf("OnStep controller (%s)", conn)
	tel.Version = "0.1.0"
	tel.Info = "onstep — OnStep Alpaca Telescope driver"

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discovery) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("onstep: invalid -discovery %q (want direct|register|off)", *discovery)
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "onstep",
		Manufacturer:        "mikefsq",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.TelescopeType, 0, tel); err != nil {
		log.Fatalf("onstep: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("onstep: serving Alpaca telescope on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("onstep: %v", err)
	}
}
