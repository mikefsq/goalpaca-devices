// Command asieaf exposes a ZWO EAF focuser as a standalone ASCOM Alpaca server,
// using the goalpaca/server (alpacadev) library and the pure-Go goasi/eaf driver.
//
// Builds pure-Go on Linux/Windows (CGO_ENABLED=0); macOS uses IOKit (cgo, on by
// default). On Linux, grant hidraw access with a udev rule:
//
//	KERNEL=="hidraw*", ATTRS{idVendor}=="03c3", MODE="0660", TAG+="uaccess"
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/asieaf-alpaca"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goasi/eaf"
)

func main() {
	port := flag.Int("port", 11112, "Alpaca HTTP port")
	index := flag.Int("focuser", 0, "EAF focuser enumeration index")
	serial := flag.String("serial", "",
		"(currently ignored — serial binding awaits an eaf.SerialNumber decode; selection is by -focuser index)")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227, no proxy needed) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// No focuser is required at startup: the driver brings the Alpaca endpoint up
	// and acquires the focuser by index whenever it appears.
	if devs, err := eaf.Enumerate(); err == nil && len(devs) > 0 {
		log.Printf("asieaf: %d EAF focuser(s) currently connected", len(devs))
	} else {
		log.Printf("asieaf: no EAF focuser yet — waiting for one to be attached")
	}

	foc := driver.NewASIFocuser(*index, *serial)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asieaf: invalid -discovery %q (want register|direct|off)", *discoveryMode)
	}
	log.Printf("asieaf: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asieaf",
		Manufacturer:        "mikefsq (ZWO EAF via goasi)",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.FocuserType, 0, foc); err != nil {
		log.Fatalf("asieaf: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asieaf: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asieaf: %v", err)
	}
	log.Printf("asieaf: shut down")
}
