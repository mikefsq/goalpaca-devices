package driver

import (
	"fmt"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	pimountgps "github.com/mikefsq/x1500_gps_12v/driver"
)

// Config overrides the board's default wiring. Unset fields keep the defaults.
type Config struct {
	TemperatureOffset float64      `json:"temperatureOffset,omitempty" alpaca:"label=Enclosure heating (°C),min=0,when=start,help=Heat added by the enclosure; subtracted from temperature with RH compensation; default 0"`
	I2CBus            string       `json:"i2cBus,omitempty" alpaca:"label=I2C bus,when=start,help=Sensor bus node; blank uses /dev/i2c-1"`
	Seconds           int          `json:"pollSeconds,omitempty" alpaca:"label=Poll interval (s),min=0,when=start,help=How often the sensors are read; 0 keeps the default 2 s"`
	Gpsd              string       `json:"gpsd,omitempty" alpaca:"label=gpsd address,when=start,help=gpsd to prefer for the GPS fix; blank uses localhost:2947; off uses only the I2C receiver"`
	Feed              []FeedTarget `json:"feed,omitempty" alpaca:"hidden"`
}

// gpsdAddr resolves the gpsd setting: blank is the default, "off" disables.
func gpsdAddr(c Config) string {
	switch c.Gpsd {
	case "":
		return defaultGpsdAddr
	case "off", "none":
		return ""
	}
	return c.Gpsd
}

func boardCfg(c Config) pimountgps.Config {
	cfg := pimountgps.DefaultConfig()
	cfg.EnclosureHeatingC = c.TemperatureOffset
	if c.I2CBus != "" {
		cfg.Bus = c.I2CBus
	}
	if c.Seconds > 0 {
		cfg.WeatherInterval = time.Duration(c.Seconds) * time.Second
		cfg.WeatherMaxAge = max(cfg.WeatherMaxAge, 5*cfg.WeatherInterval)
	}
	return cfg
}

func init() {
	registry.Register(registry.Driver{
		Name:        "pi_mount_gps",
		Type:        alpacadev.ObservingConditionsType,
		Description: "pi_mount_gps board weather (SHT45, BMP580) and GPS",
		ConfigExample: `{ "driver": "pi_mount_gps", ` +
			`"feed": [ { "addr": "10.0.1.5:11111", "type": "telescope", "device": 0 } ] }`,
		Config: func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewDriver(boardCfg(cfg), gpsdAddr(cfg))
			if _, err := d.actionTemperatureOffset(fmt.Sprint(cfg.TemperatureOffset)); err != nil {
				return nil, err
			}
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			if err := d.SetFeedTargets(cfg.Feed); err != nil {
				return nil, err
			}
			return d, nil
		},
	})
}
