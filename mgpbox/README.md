# mgpbox

ASCOM Alpaca ObservingConditions driver for the Astromi.ch MGPBox / MGPBox v2,
using [astromi.ch](https://github.com/mikefsq/astromi.ch).

## Build and run

```sh
go build -o mgpbox ./cmd/mgpbox
./mgpbox
./mgpbox -serial YOUR_BRIDGE_SERIAL -port 11125
```

Without a serial, the driver discovers an MGPBox by its data stream. Use the
FTDI bridge serial to select a specific unit. The default HTTP port is `11111`.
See `-help` and the [shared configuration instructions](../README.md#configuration).

## Measurements and Actions

The driver reports Temperature (°C), Humidity (%), Pressure (hPa), and
DewPoint (°C). Readings come from the latest streamed sample;
`TimeSinceLastUpdate` reports its age. Other weather sensors are unsupported.

Action names are case-insensitive. GPS and calibration data are available
through these device-specific Actions:

| Action | Result |
|--------|--------|
| `Temperature` / `Humidity` / `Pressure` / `DewPoint` | weather scalars (also OC properties) |
| `DewOffset` / `DewPWM` | dew-heater state |
| `Latitude` / `Longitude` / `Altitude` / `Satellites` / `FixQuality` / `FixType` / `PDOP` / `HDOP` / `VDOP` / `GpsTime` | GPS scalars |
| `Gps` | whole fix as JSON: `{valid,time,latitude,longitude,altitude,satellites,fixQuality,fixType,pdop,hdop,vdop}` |
| `Pcal` / `Tcal` / `Hcal` | calibration scalars |
| `Calibration` | stored calibration + streaming flags, as JSON |
| `GpsEnable` | GPS power: `true`/`false` sets it; empty reads the last-commanded state |
| `RebootGps` | restart the GPS module → `ok` |

`Gps` reports `valid:false` until the receiver has a fix.

## Weather and GPS feed

Configure `feed` in the device file to send conditions to telescopes or
power boards that accept `SetEnvironment`:

```jsonc
{
  "driver": "mgpbox",
  "port": 11125,
  "feed": [
    {"addr": "192.168.1.50:11200", "type": "telescope", "device": 0},
    {"addr": "localhost:11130", "type": "switch", "device": 0}
  ]
}
```

The feed includes weather on each update and position/time only with a GPS
fix. Each target uses the fields it supports. The 10Micron driver filters
small changes and limits clock synchronization to once per hour.

`Feed` reads or replaces the target list; `PushFeed` requests an
immediate update. `mountAddr` and `mountDevice` remain accepted for a single
telescope target.
