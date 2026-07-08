# mgpbox — Alpaca ObservingConditions

ASCOM Alpaca **ObservingConditions** device for the **Astromi.ch MGPBox / MGPBox v2**
(GPS + weather + dew-heater box), over the Go [`mikefsq/astromi.ch`](../../astromi.ch)
`mgpbox` library (FTDI USB-serial). Sibling of the other `goalpaca-devices` drivers;
served standalone by `cmd/mgpbox` or hosted by `astrofleet`.

Hardware-validated end-to-end against a live MGPBox v2 (FT231X, serial `D30B0DP6`): the
server auto-acquires the box and serves live Temperature / Humidity / Pressure / DewPoint.

## Sensor mapping

The MGPBox reports four ambient sensors, mapped directly to ASCOM; everything else stays
at the `BaseObservingConditions` *NotImplemented* default (the box has no cloud/wind/rain/
sky sensors, and its GPS has no ASCOM ObservingConditions home).

| ASCOM property | Source |
|----------------|--------|
| `Temperature`  | ambient temperature (°C) |
| `Humidity`     | relative humidity (%RH) |
| `Pressure`     | barometric pressure (hPa) |
| `DewPoint`     | dew point (°C) |

The box **streams**, so `Refresh` is a no-op (reads are served from the freshest sample)
and `TimeSinceLastUpdate` returns the age of the latest sample.

## Measurements via ASCOM Actions

**Every measurement the box makes** is reachable as a **scalar Action**, plus whole-object
JSON for GPS and calibration. (The GPS and calibration data have no home in ASCOM
ObservingConditions; the weather scalars duplicate the standard OC properties so a client
can read everything through one uniform interface.) Names are advertised in CamelCase but
matched **case-insensitively** — send any casing.

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

```
curl -X PUT .../observingconditions/0/action -d 'Action=Gps&Parameters='
→ {"valid":true,"latitude":37.959428,"longitude":-122.558518,"altitude":26.3,"satellites":4,...}
```

`gps` returns `valid:false` (and zeroed position) until the receiver has a fix.

## Feeding a 10Micron mount

The MGPBox can push its GPS + weather to a **tenmicron mount's** Alpaca server, so the
mount's site coordinates, clock, and refraction pressure/temperature track the box. Set
the mount's `host:port` (its telescope device number defaults to 0):

- CLI: `./mgpbox -mount 10.0.1.60:11110`
- Fleet config: `"mountAddr": "10.0.1.60:11110"` (and optional `"mountDevice": 0`)
- At runtime: the `mountfeed` Action — `Parameters=host:port` (or `host:port/N`) to set,
  empty to read, `off` to disable; `pushmount` forces an immediate push.

Every `feedInterval` (30s) the driver POSTs its current snapshot as JSON to the mount's
`setenvironment` Action:

```json
{"pressure_hpa":1015.5,"temperature_c":24.0,"humidity_pct":45,"dewpoint_c":13.2,
 "latitude":37.9594,"longitude":-122.5585,"elevation_m":26.3,"time":"2026-07-08T17:16:43Z"}
```

Site/position/time are included **only when the GPS has a real fix** (so an unlocked
receiver never sends a 0,0 position or an unsynced clock). The mgpbox always sends its full
snapshot; the **tenmicron driver does the diffing** — it applies a field to the mount only
when it changes beyond a threshold (so GPS jitter never churns the surveyed site) and syncs
the clock at most hourly. `humidity`/`dewpoint` are sent too and currently ignored by the
mount (available for future use).

## Run

```
go build ./cmd/mgpbox
./mgpbox                          # discover, bind the first MGPBox (no serial needed)
./mgpbox -serial D30B0DP6         # pin a specific unit by FTDI-bridge serial
./mgpbox -port 11125 -discovery off
```

`serial` is optional — the library's `Discover()` probes the FTDI ports and identifies the
MGPBox by its streamed content (so it's not confused with a Unihedron SQM on the same VID).
Pass a serial to pin a unit or get a stable ASCOM `UniqueID` (without one it is
index-derived).

## Notes

- The `astromi.ch` library is not yet published; this module's `go.mod` carries a local
  `replace` to `../../astromi.ch` (and the dev `go.work` lists the sibling checkout). Pin
  to a pushed pseudo-version once the library is published, like the other devices.
