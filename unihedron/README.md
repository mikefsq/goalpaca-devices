# unihedron — Alpaca ObservingConditions

ASCOM Alpaca **ObservingConditions** device for **Unihedron Sky Quality Meters**
(SQM-LU / SQM-LU-DL / SQM-LE), over the Go [`mikefsq/unihedron`](../../unihedron)
library (FTDI USB-serial). Sibling of the other `goalpaca-devices` drivers; served
standalone by `cmd/unihedron` or hosted by `astrofleet`.

Hardware-validated end-to-end against a live SQM-LU (unit serial 5533): the server
auto-acquires the meter and serves `SkyQuality` ≈ 10.95 mag/arcsec² and `Temperature`
≈ 27.7 °C over the Alpaca HTTP API.

## Sensor mapping

An SQM measures two things, so the driver implements exactly two ASCOM sensors and
leaves the rest at the `BaseObservingConditions` *NotImplemented* default:

| ASCOM property | Source | Notes |
|----------------|--------|-------|
| `SkyQuality`   | SQM reading (mag/arcsec²) | Exact — the ASCOM property is *defined* in these units. |
| `Temperature`  | SQM light-sensor temperature (°C) | Ambient-ish enclosure temperature. |

Deliberately **not** implemented:

- `SkyBrightness` — ASCOM unit is Lux; converting from mag/arcsec² is band/model-
  dependent, so we don't fabricate it. `SkyQuality` is the honest field.
- `SkyTemperature` — implies an IR cloud sensor; the SQM has none.
- Humidity / pressure / wind / rain / cloud / dewpoint / FWHM — no such sensors.

`Refresh` forces a fresh reading; a short (2 s) cache lets a client's `Refresh` +
several property GETs share one serial round-trip. `TimeSinceLastUpdate` returns the
cache age for the supported sensors (`-1` before the first read); `SensorDescription`
describes the backing hardware.

## Run

```
go build ./cmd/unihedron
./unihedron                       # probes FTDI ports, binds the first SQM found (no serial needed)
./unihedron -serial AG0JWD3W      # pin a specific unit by FTDI-bridge serial, or...
./unihedron -serial 5533          # ...by the meter's own unit serial number
./unihedron -port 11124 -discovery off
```

A `serial` is **optional**: the library's `Discover()` probes each FTDI port with `ix`
and keeps only the ones that answer as a Unihedron, so a bare device reliably finds the
SQM even alongside other FTDI devices (e.g. a Pegasus focuser, which shares VID 0x0403 —
on Linux its port simply reports busy and is skipped). Pass a serial only to pin a
specific unit (useful in the rare multi-SQM setup, or for a stable ASCOM `UniqueID` —
without one the `UniqueID` is index-derived).

Then e.g.:

```
curl 'http://127.0.0.1:11124/api/v1/observingconditions/0/skyquality?ClientID=1&ClientTransactionID=1'
```
