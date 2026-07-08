// Command unihedron runs a Unihedron Sky Quality Meter as a standalone ASCOM Alpaca
// ObservingConditions server.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/goalpaca-devices/unihedron"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11124, "Alpaca HTTP port")
	index := flag.Int("device", 0, "SQM enumeration index (when -serial is unset)")
	serial := flag.String("serial", "", "bind to the SQM with this USB-bridge serial (stable across replug); overrides -device")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	var oc *driver.SQM
	if s := strings.TrimSpace(*serial); s != "" {
		oc = driver.NewSQMBySerial(0, s)
	} else {
		oc = driver.NewSQM(*index)
	}

	srv := alpacadev.New(alpacadev.Config{AlpacaPort: *port, Discovery: discovery(*dmode, *dsrv, *ipv6), ServerName: "unihedron", Manufacturer: "mikefsq"})
	if err := srv.Register(alpacadev.ObservingConditionsType, 0, oc); err != nil {
		log.Fatalf("unihedron: register: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("unihedron: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("unihedron: %v", err)
	}
}

func discovery(mode, server string, ipv6 bool) alpacadev.DiscoveryConfig {
	switch strings.ToLower(mode) {
	case "off":
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: server}
	default:
		return alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: ipv6}
	}
}
