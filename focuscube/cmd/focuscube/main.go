// Command focuscube runs the Pegasus FocusCube as a standalone Alpaca server.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/focuscube-alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11121, "Alpaca HTTP port")
	index := flag.Int("focuser", 0, "FocusCube enumeration index (when -serial is unset)")
	serial := flag.String("serial", "", "bind to the FocusCube with this USB serial (stable across replug); overrides -focuser")
	maxStep := flag.Int("maxstep", 100000, "maximum step (host-side)")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	var foc *driver.PegasusFocuser
	if s := strings.TrimSpace(*serial); s != "" {
		foc = driver.NewPegasusFocuserBySerial(0, s, *maxStep)
	} else {
		foc = driver.NewPegasusFocuser(*index, *maxStep)
	}

	srv := alpacadev.New(alpacadev.Config{AlpacaPort: *port, Discovery: discovery(*dmode, *dsrv, *ipv6), ServerName: "focuscube", Manufacturer: "mikefsq"})
	if err := srv.Register(alpacadev.FocuserType, 0, foc); err != nil {
		log.Fatalf("focuscube: register: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("focuscube: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("focuscube: %v", err)
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
