// Command asiefw exposes a ZWO EFW filter wheel as a standalone ASCOM Alpaca
// server, using the goalpaca/server (alpacadev) library and the goasi/efw
// pure-Go HID driver — no ZWO SDK runtime dependency.
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

	alpacadev "github.com/mikefsq/goalpaca/server"
	driver "github.com/mikefsq/asiefw-alpaca"
	"github.com/mikefsq/goasi/efw"
)

func main() {
	port := flag.Int("port", 11113, "Alpaca HTTP port")
	index := flag.Int("wheel", 0, "EFW filter wheel index (used only when -serial is empty)")
	serial := flag.String("serial", "",
		"bind the wheel with this serial (hex); recommended for multi-device and start-before-plug")
	unidirectional := flag.Bool("unidirectional", false,
		"unidirectional moves (always same rotation direction) for repeatable filter seating; default bidirectional (shortest path)")
	discoveryMode := flag.String("discovery", "direct",
		"discovery mode: direct (self-answer on 32227, no proxy needed) | register (heartbeat to discovery_proxy) | off")
	discoveryServer := flag.String("discovery-server", "localhost:32227",
		"discovery_proxy address for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery (direct mode)")
	flag.Parse()

	// Do NOT require a wheel at startup: the service may be started before the
	// device is plugged in. The driver brings the Alpaca endpoint up and acquires
	// the wheel (by serial) whenever it appears.
	if devs, err := efw.Enumerate(); err == nil && len(devs) > 0 {
		log.Printf("asiefw: %d EFW filter wheel(s) currently connected", len(devs))
	} else {
		log.Printf("asiefw: no EFW filter wheel yet — waiting for one to be attached")
	}

	wheel := driver.NewASIFilterWheel(*index, *serial, *unidirectional)

	var disc alpacadev.DiscoveryConfig
	switch strings.ToLower(*discoveryMode) {
	case "direct":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryDirect, EnableIPv6: *ipv6}
	case "off":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}
	case "register":
		disc = alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryRegister, ServerAddr: *discoveryServer}
	default:
		log.Fatalf("asiefw: invalid -discovery %q (want register|direct|off)", *discoveryMode)
	}
	log.Printf("asiefw: discovery mode = %s", strings.ToLower(*discoveryMode))

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:          *port,
		Discovery:           disc,
		ServerName:          "asiefw",
		Manufacturer:        "mikefsq (ZWO EFW via goasi)",
		ManufacturerVersion: "0.1.0",
	})
	if err := srv.Register(alpacadev.FilterWheelType, 0, wheel); err != nil {
		log.Fatalf("asiefw: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("asiefw: serving Alpaca on :%d (Ctrl-C to stop)", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("asiefw: %v", err)
	}
	log.Printf("asiefw: shut down")
}
