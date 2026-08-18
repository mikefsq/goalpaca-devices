// Command ptpcam runs a Fujifilm or Sony stills camera as a standalone ASCOM
// Alpaca camera server, over PTP with no vendor SDK.
//
// Camera selection (the opener and the liveness probe) lives in the driver
// package, which this shares with the alpacahurd registry entry so the two
// cannot drift. Importing it also pulls in ptp/fuji and ptp/sony, whose init
// functions register the vendors — that registration is what makes their bodies
// visible to USB enumeration at all.
//
// It serves Alpaca and nothing else: one port, no side channels. An earlier
// revision exposed the untouched camera file and the live preview on plain HTTP
// routes, which were not part of the Alpaca specification and have been removed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/goalpaca-devices/ptpcam"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/ptp/usb"
)

func main() {
	port := flag.Int("port", 11125, "Alpaca HTTP port")
	serial := flag.String("serial", "", "USB serial of the body to open (required when more than one is attached)")
	vendor := flag.String("vendor", "auto", "which body to open: auto | fuji | sony")
	size := flag.String("size", "", "sensor size as WxH, when the camera does not report it (Sony)")
	pixel := flag.Float64("pixel-size", 0, "photosite pitch in microns; PTP cannot report it, and a client needs it to compute image scale (X-T5 3.04, A7R V 3.76)")
	list := flag.Bool("list", false, "list attached cameras and exit")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	flag.Parse()

	if *list {
		if err := listCameras(); err != nil {
			log.Fatalf("ptpcam: %v", err)
		}
		return
	}

	cam := driver.New("ptpcam-0", "PTP Camera", driver.NewOpener(*vendor, *serial))
	// The liveness probe reads the OS USB registry and never touches the open
	// camera — a probe that sent PTP traffic would compete with an exposure in
	// flight, which on a body mid-capture is exactly the wrong moment.
	cam.AliveFn = driver.NewAliveProbe(*vendor, *serial)
	if *size != "" {
		w, h, err := parseWxH(*size)
		if err != nil {
			log.Fatalf("ptpcam: -size: %v", err)
		}
		cam.SetGeometry(w, h)
	}
	if *pixel > 0 {
		cam.SetPixelSize(*pixel)
	} else {
		log.Printf("ptpcam: -pixel-size not given, so PixelSizeX/Y report 0; " +
			"a client cannot compute image scale or plate-solve without it")
	}

	srv := alpacadev.New(alpacadev.Config{
		AlpacaPort:   *port,
		Discovery:    discovery(*dmode, *dsrv, *ipv6),
		ServerName:   "ptpcam",
		Manufacturer: "mikefsq",
	})
	if err := srv.Register(alpacadev.CameraType, 0, cam); err != nil {
		log.Fatalf("ptpcam: register: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("ptpcam: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("ptpcam: %v", err)
	}
}

func listCameras() error {
	devs, err := usb.Enumerate()
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("no cameras found")
		return nil
	}
	for _, d := range devs {
		fmt.Printf("  %s\n", d)
	}
	return nil
}

func parseWxH(s string) (int, int, error) {
	var w, h int
	if _, err := fmt.Sscanf(strings.ToLower(s), "%dx%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("%q is not WxH", s)
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("%q has a non-positive dimension", s)
	}
	return w, h, nil
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
