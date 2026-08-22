// The _linux suffix is this driver's platform declaration: the I2C, SPI, and
// UART buses it drives exist only on Linux SBCs, so registration compiles only
// there. On every other platform the package still builds (a fat alpacahurd
// blank-imports it anywhere) but registers nothing, so the driver is absent
// from that host's registry and a device entry naming it degrades to the
// skipped-entry path. See alpacahurd's DRIVERS.md, "Platform-specific drivers".
package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/stellarmate"
)

// The two SM Pro devices open disjoint hardware, so each gets its own Hub over
// its own subsystems: the Switch drives the I2C expander, DAC, SPI ADC, dew PWM
// and status LED, the Focuser drives the TMC2209 UART and nothing else. Nothing
// is shared between them, so they run equally well in one process or two.

// SwitchConfig and FocuserConfig are each device's driver-owned keys. Every
// field overrides one part of the wiring stellarmate detects for the compute
// module; a key left out keeps the detected value, which is right on both the
// CM4 and the CM5 with no entry at all.
//
// The dew-heater PWM chip is deliberately not a key. It is the one field that
// moves between modules (pwmchip2 on the CM4, pwmchip0 on the CM5), and
// detecting it is exactly what stellarmate.DefaultConfig does; exposing it would
// offer a setting whose only correct value is the one already chosen, with 0 a
// legitimate value that could not be told from "unset".
type SwitchConfig struct {
	I2CBus string `json:"i2cBus,omitempty" alpaca:"label=I2C bus,when=start,help=Expander/DAC/EEPROM bus node; blank uses the detected /dev/i2c-1"`
	SPIDev string `json:"spiDev,omitempty" alpaca:"label=ADC SPI device,when=start,help=Voltage-sensing ADC node; blank uses the detected spidev"`
}

// FocuserConfig is the Focuser entry's keys. The stepper is on its own UART and
// shares nothing with the Switch.
type FocuserConfig struct {
	StepperDev string `json:"stepperDev,omitempty" alpaca:"label=Stepper UART,when=start,help=TMC2209 serial node; blank uses the detected /dev/ttyAMA2"`
	FocusMax   int    `json:"focusMax,omitempty" alpaca:"label=Max position,min=0,when=start,help=Travel limit in steps; 0 keeps the default 100000"`
	FocusSpeed int    `json:"focusSpeed,omitempty" alpaca:"label=Speed,min=0,when=start,help=Move speed; 0 keeps the default"`
}

// switchCfg and focuserCfg fold an entry's overrides onto the detected wiring.
func switchCfg(c SwitchConfig) stellarmate.Config {
	cfg := stellarmate.DefaultConfig()
	if c.I2CBus != "" {
		cfg.I2CBus = c.I2CBus
	}
	if c.SPIDev != "" {
		cfg.SPIDev = c.SPIDev
	}
	return cfg
}

func focuserCfg(c FocuserConfig) stellarmate.Config {
	cfg := stellarmate.DefaultConfig()
	if c.StepperDev != "" {
		cfg.StepperDev = c.StepperDev
	}
	if c.FocusMax > 0 {
		cfg.FocusMax = uint32(c.FocusMax)
	}
	if c.FocusSpeed > 0 {
		cfg.FocusSpeed = c.FocusSpeed
	}
	return cfg
}

// init registers both SM Pro devices in the goalpaca driver registry, so a
// composed host (alpacahurd) can construct them from config entries by
// importing this package.
func init() {
	registry.Register(registry.Driver{
		Name:          "smpro-switch",
		Type:          alpacadev.SwitchType,
		Description:   "StellarMate SM Pro power/dew/variable-output switch",
		ConfigExample: `{ "driver": "smpro-switch" }`,
		Config:        func() any { return &SwitchConfig{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg SwitchConfig
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewSwitch(NewHub(switchCfg(cfg), stellarmate.SubsystemsSwitch))
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
	registry.Register(registry.Driver{
		Name:          "smpro-focuser",
		Type:          alpacadev.FocuserType,
		Description:   "StellarMate SM Pro TMC2209 focuser",
		ConfigExample: `{ "driver": "smpro-focuser" }`,
		Config:        func() any { return &FocuserConfig{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg FocuserConfig
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewFocuser(NewHub(focuserCfg(cfg), stellarmate.SubsystemsFocuser))
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
