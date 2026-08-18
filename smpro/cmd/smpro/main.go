// Command smpro runs the StellarMate SM Pro controller board as a standalone
// Alpaca server: one Switch device (power, dew, variable output, sensors) and
// one Focuser device (the TMC2209), sharing one board handle.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	driver "github.com/mikefsq/goalpaca-devices/smpro"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

func main() {
	port := flag.Int("port", 11130, "Alpaca HTTP port")
	dmode := flag.String("discovery", "direct", "discovery mode: direct | register | off")
	dsrv := flag.String("discovery-server", "localhost:32227", "discovery proxy for register mode")
	ipv6 := flag.Bool("ipv6", false, "also answer IPv6 multicast discovery")
	cfg := stellarmate.DefaultConfig()
	flag.StringVar(&cfg.I2CBus, "i2c", cfg.I2CBus, "I2C bus device")
	flag.StringVar(&cfg.SPIDev, "spi", cfg.SPIDev, "SPI (ADC) device")
	flag.IntVar(&cfg.PWMChip, "pwmchip", cfg.PWMChip, "sysfs PWM chip number")
	flag.StringVar(&cfg.StepperDev, "tty", cfg.StepperDev, "TMC2209 UART device")
	flag.Parse()

	hub := driver.NewHub(cfg)
	srv := alpacadev.New(alpacadev.Config{AlpacaPort: *port, Discovery: discovery(*dmode, *dsrv, *ipv6), ServerName: "smpro", Manufacturer: "mikefsq"})
	if err := srv.Register(alpacadev.SwitchType, 0, driver.NewSwitch(hub)); err != nil {
		log.Fatalf("smpro: register switch: %v", err)
	}
	if err := srv.Register(alpacadev.FocuserType, 0, driver.NewFocuser(hub)); err != nil {
		log.Fatalf("smpro: register focuser: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("smpro: serving Alpaca on :%d", *port)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("smpro: %v", err)
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
