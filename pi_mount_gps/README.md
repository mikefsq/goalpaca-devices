# pi_mount_gps

ASCOM Alpaca ObservingConditions driver and environment feed for the
pi_mount_gps CM5 board, using [pi_mount_gps](https://github.com/mikefsq/pi_mount_gps) over I2C.

## Build and run

This driver currently requires the hardware-library checkout at
`../pi_mount_gps` relative to the repository root, as recorded in `go.mod`.
From this driver directory:

```sh
go build -o pi_mount_gps ./cmd/pi_mount_gps
./pi_mount_gps
```

The board must be on `/dev/i2c-1` (override with `i2cBus`), and the user
needs to be in the `i2c` group. The default HTTP port is `11111`. See `-help`
and the [shared configuration instructions](../README.md#run).

## Readings

| Property | Measurement |
|---|---|
| `Temperature` | SHT45 air temperature in °C |
| `Humidity` | SHT45 relative humidity in % |
| `DewPoint` | Dew point from temperature and humidity (Magnus) in °C |
| `Pressure` | BMP580 station pressure in hPa (not reduced to sea level) |

Other weather properties are unsupported. The board is read in the background
every 2 s (`pollSeconds`), and property reads are served from that cache.
`Refresh` reads the sensors now. Readings older than 30 s (or 5 polls, whichever
is longer) return `ValueNotSet`, and the driver reopens the board.
`TimeSinceLastUpdate` reports the sample age, or `-1` before the first reading.
`AveragePeriod` is 0; only 0 can be set.

## GPS

The fix comes from gpsd when it has a current one (`localhost:2947`; set
`gpsd` to another address, or `off`), otherwise from the board's u-blox
receiver over I2C. The driver never opens the receiver's UART or PPS, which
belong to gpsd and chrony. `GpsSource` reports which source is in use.

Weather sensors are opened independently. With gpsd enabled, the driver allows
five seconds for its first usable fix before opening the I2C GPS fallback.
A fix older than five seconds (or loss of fix) activates fallback. The driver
checks once per second and closes the I2C GPS, stopping its polling loop, when
gpsd has a usable fix again. Failed I2C opens retry every ten seconds. With gpsd
disabled, I2C GPS opens immediately. Shutdown closes both sensor handles.

Actions match the `mgpbox` driver's names and formats:

| Action | Result |
|---|---|
| `Temperature` / `Humidity` / `Pressure` / `DewPoint` | weather scalars (same values as the properties) |
| `Latitude` / `Longitude` / `Altitude` / `Satellites` / `FixQuality` / `FixType` / `PDOP` / `HDOP` / `VDOP` / `GpsTime` | GPS scalars |
| `Gps` | whole fix as JSON: `{valid,time,latitude,longitude,altitude,satellites,fixQuality,fixType,pdop,hdop,vdop}` |
| `TimeSource` | where the feed's time comes from: `chrony`, `gps` or `none` |
| `GpsSource` | where the fix comes from: `gpsd`, `i2c` or `none` |

`Gps` reports `valid:false` while there is no current fix. `FixQuality` is the
GGA digit (`0` none, `1` GPS, `2` DGPS) and `FixType` the GSA digit (`1` none,
`2` 2D, `3` 3D); from gpsd they are mapped to the same digits. From I2C, the
dilutions and fix type lag the position by one second.

## Weather and GPS feed

Configure `feed` in the device file to send conditions to telescopes or
power boards that accept `SetEnvironment`, such as the 10Micron mount:

```jsonc
{
  "driver": "pi_mount_gps",
  "feed": [
    {"addr": "192.168.1.50:11200", "type": "telescope", "device": 0}
  ]
}
```

Every 30 s the driver sends `pressure_hpa`, `temperature_c`, `humidity_pct` and
`dewpoint_c` while the readings are fresh. It sends `latitude`, `longitude` and
`elevation_m` only with a current GPS fix (elevation only with a 3D fix). Values the board cannot measure are
left out rather than sent as zero.

`time` is the system clock when chrony reports it synchronised
(`chronyc tracking` leap status `Normal`), otherwise the GPS fix time, and is
left out when neither is available. Changes of time source are logged.

`Feed` reads or replaces the target list (`host:port[/type[/device]]`,
comma-separated, or `off`). `PushFeed` sends now, ignoring retry backoff.
`FeedStatus` reports each target's health. A failing target is retried with
backoff from 30 s up to 15 min. `DeviceState` includes `FeedTargets` and
`FeedFailures`.

```sh
curl 'http://localhost:11111/api/v1/observingconditions/0/dewpoint?ClientID=1&ClientTransactionID=1'
```

## Enclosure heating compensation

Set `"temperatureOffset": 0` in the device configuration (default zero). A
positive value is enclosure-added heat in °C: `5` subtracts 5°C and compensates
relative humidity at constant vapor pressure. Dew point is preserved; RH is
capped at 100%. Raw barometer temperature and pressure are unchanged.

The `TemperatureOffset` Alpaca action reads the current value with empty
parameters, or sets it with a numeric parameter such as `5` or `0`. Cached
weather changes immediately, without extra I2C traffic. Changes survive sensor
reconnection but are runtime-only: edit the device configuration to retain them
across driver restarts.
