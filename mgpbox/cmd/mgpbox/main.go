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
	feed := flag.String("feed", "", "comma-separated Alpaca endpoints to push GPS+weather to, each\n"+
		"host:port[/type[/device]] — type is telescope (default) or switch, e.g.\n"+
		"10.0.1.5:11111/telescope/0,localhost:11130/switch/0  (empty = off)")
	mount := flag.String("mount", "", "host:port of a tenmicron mount Alpaca server to feed (deprecated: use -feed)")
	mountDev := flag.Int("mount-device", 0, "the mount server's telescope device number (with -mount)")
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

	// -feed is the general form; -mount is the historical single-telescope flag. Given
	// both, -feed wins and -mount is appended, so an old command line keeps working.
	targets, err := driver.ParseFeedTargets(*feed)
	if err != nil {
		log.Fatalf("mgpbox: -feed: %v", err)
	}
	if a := strings.TrimSpace(*mount); a != "" {
		targets = append(targets, driver.FeedTarget{Addr: a, Type: "telescope", Device: *mountDev})
	}
	if err := oc.SetFeedTargets(targets); err != nil {
		log.Fatalf("mgpbox: feed: %v", err)
	}
	for _, t := range targets {
		log.Printf("mgpbox: feeding environment to %s", t)
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
