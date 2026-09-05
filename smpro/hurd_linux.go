// Registration is Linux-only; other platforms import the package without registering devices.
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

// SwitchConfig overrides detected switch wiring. Unset fields retain board defaults.
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
