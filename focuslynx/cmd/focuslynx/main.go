// Command focuslynx runs one Optec FocusLynx channel as a standalone Alpaca server.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/focuslynx-alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func main() {
	port := flag.Int("port", 11122, "Alpaca HTTP port")
	nickname := flag.String("nickname", "",
		"comma-separated focuser nicknames — one Alpaca device per nickname, in order "+
			"(stable, plug-order- and platform-independent). Empty = index mode via -hub/-channel.")
	index := flag.Int("hub", 0, "FocusLynx hub enumeration index (index mode)")
	channel := flag.Int("channel", 1, "focuser channel: 1 (F1) or 2 (F2) (index mode)")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()
	if *channel != 1 && *channel != 2 {
		log.Fatalf("focuslynx: -channel must be 1 or 2")
	}

	srv := alpacadev.New(alpacadev.Config{AlpacaPort: *port, Discovery: discovery(*dmode, *dsrv, *ipv6), ServerName: "focuslynx", Manufacturer: "mikefsq"})

	// Each nickname becomes one Alpaca focuser device (0,1,…); its hub/channel is
	// resolved over the protocol at connect time. No nicknames: fall back to a single
	// enumeration-index-bound device.
	if nl := strings.TrimSpace(*nickname); nl != "" {
		for i, nk := range strings.Split(nl, ",") {
			if nk = strings.TrimSpace(nk); nk == "" {
				continue
			}
			dev := driver.NewOptecFocuserByNickname(i, nk)
			if err := srv.Register(alpacadev.FocuserType, i, dev); err != nil {
				log.Fatalf("focuslynx: register device %d (%s): %v", i, nk, err)
			}
			log.Printf("focuslynx: registered focuser device %d (%s)", i, dev.ID)
		}
	} else if err := srv.Register(alpacadev.FocuserType, 0, driver.NewOptecFocuser(*index, *channel)); err != nil {
		log.Fatalf("focuslynx: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("focuslynx: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("focuslynx: %v", err)
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
