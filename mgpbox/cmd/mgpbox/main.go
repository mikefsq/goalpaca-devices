// Command mgpbox runs an Astromi.ch MGPBox as a standalone ASCOM Alpaca
// ObservingConditions server.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/goalpaca-devices/mgpbox"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11125, "Alpaca HTTP port")
	index := flag.Int("device", 0, "MGPBox discovery index (when -serial is unset)")
	serial := flag.String("serial", "", "bind to the MGPBox with this FTDI USB-bridge serial (stable across replug); overrides -device")
	mount := flag.String("mount", "", "host:port of a tenmicron mount Alpaca server to feed GPS+weather to (empty = off)")
	mountDev := flag.Int("mount-device", 0, "the mount server's telescope device number")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	var oc *driver.MGPBox
	if s := strings.TrimSpace(*serial); s != "" {
		oc = driver.NewMGPBoxBySerial(0, s)
	} else {
		oc = driver.NewMGPBox(*index)
	}
	if a := strings.TrimSpace(*mount); a != "" {
		oc.SetMountFeed(a, *mountDev)
	}

	srv := alpacadev.New(alpacadev.Config{AlpacaPort: *port, Discovery: discovery(*dmode, *dsrv, *ipv6), ServerName: "mgpbox", Manufacturer: "mikefsq"})
	if err := srv.Register(alpacadev.ObservingConditionsType, 0, oc); err != nil {
		log.Fatalf("mgpbox: register: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("mgpbox: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("mgpbox: %v", err)
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
