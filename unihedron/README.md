# unihedron

ASCOM Alpaca ObservingConditions driver for Unihedron Sky Quality Meters,
using [unihedron](https://github.com/mikefsq/unihedron) over USB-serial.

## Build and run

```sh
go build -o unihedron ./cmd/unihedron
./unihedron
./unihedron -serial 5533 -port 11124
```

Without a serial, the driver probes serial adapters and selects an SQM.
Use the meter's unit serial or its USB bridge serial to select a specific
unit. The default HTTP port is `11111`. See `-help` and the
[shared configuration instructions](../README.md#configuration).

## Readings

| Property | Measurement |
|---|---|
| `SkyQuality` | Sky brightness in mag/arcsec² |
| `Temperature` | Light-sensor temperature in °C |

Other weather properties are unsupported. `Refresh` requests a fresh
reading; ordinary reads share a ten-second cache, refreshed by background polling. `TimeSinceLastUpdate`
reports the sample age, or `-1` before the first reading.

```sh
curl 'http://localhost:11124/api/v1/observingconditions/0/skyquality?ClientID=1&ClientTransactionID=1'
```
