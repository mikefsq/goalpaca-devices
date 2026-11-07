# tenmicron

A standalone ASCOM Alpaca Telescope server for 10Micron GM-series mounts,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the
[`lx200/tenmicron`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build -o tenmicron ./cmd/tenmicron
```

## Run

```sh
./tenmicron -addr 10.0.1.51:3492
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-addr` | "" (required) | mount TCP address `host:port` (10Micron uses 3490/3492) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

## Environment feed (refraction / site / time)

Another driver (an environment/GPS feeder) can push observing-site data into the
mount over the standard, stateless Alpaca port:

- Site latitude/longitude/elevation and UTC date are standard ASCOM
  Telescope members — `PUT sitelatitude`, `sitelongitude`, `siteelevation`,
  `utcdate`.
- Refraction pressure/temperature, the optical train, and dual-axis tracking
  are not standard Telescope members, so they are exposed as ASCOM Actions (advertised
  in CamelCase, matched case-insensitively; see `GET supportedactions`). The read/write ones
  follow the fleet convention — empty `Parameters` reads, a value writes:

| Action | Parameters | Effect |
|---|---|---|
| `RefractionPressure` | empty reads (hPa) · `<hPa>` sets (`:SRPRS`) | reads back the last-set value (or the mount's, first time) |
| `RefractionTemperature` | empty reads (°C) · `<°C>` sets (`:SRTMP`) | reads back the last-set value |
| `Optics` | empty reads JSON · JSON sets | instrument profile (aperture/focal length, mm); reads back the last set |
| `DualAxisTracking` | empty reads bool · `true`/`false` sets | drive both axes to follow the refraction/pointing model |
| `SetEnvironment` | JSON (all fields optional) | bulk write; applies each present field |

`SetEnvironment` is a one-call convenience taking any subset (JSON-input only):

```
PUT /api/v1/telescope/0/action
Action=SetEnvironment&Parameters={"pressure_hpa":1013.2,"temperature_c":-3.0,
  "latitude":47.3,"longitude":8.5,"elevation_m":540,"time":"2026-06-02T21:00:00Z"}
```

The driver diffs each field against the last applied (thresholded, so sensor noise /
GPS jitter doesn't churn the mount) and syncs the clock at most hourly; it returns
`{"applied":[…],"skipped":[…]}`. Reading `RefractionPressure`/`RefractionTemperature`
reflects a value set either way. Setting the refraction datums does not enable refraction —
set the standard `doesrefraction` member for that.

Set `lx200Port` in the device file to serve an LX200 TCP endpoint.
Set `aperture` and `focalLength` in the file in millimetres.

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).

### Precise host-clock synchronization

`Action=SyncClock` with empty `Parameters` performs a one-shot TenMicron clock
synchronization. Alternatively include `"sync_clock":true` in `SetEnvironment`
alongside pressure/temperature/site fields. Do not include `time` with
`sync_clock`: the precision reference is the driver's own host clock, not a
client timestamp transported over Alpaca. Existing `time` writes keep their
absolute-set behavior. Synchronization is explicit, not a background loop.

The driver detects the running Chrony or ntpd client and queries its local
statistics (`chronyc` or `ntpq` must be available to the service account). It
requires synchronized time with estimated error at most **0.1 ms (100 µs)**,
including offset, half root delay, and root dispersion. Stratum alone does not
qualify the host. No hardware access or GPS receiver ownership is added.
Unsupported/unavailable statistics, simultaneous time clients, an active mount
GPS time source, or slewing prevent a correction.

Each round takes seven fresh `:GJD2#` samples, timestamped inside the LX200 wire
lock immediately before sending and after receiving, before parsing. At least
three replies must take at most 500 ms; leap-second replies are rejected. The
median of mount timestamp minus host receipt time estimates the offset. The
observed ~110 ms request delay is primarily before timestamp capture, so half
that delay is **not** subtracted. Sample spread above 30 ms rejects synchronization.

Uncertainty is an operational estimate: a **15 ms assumed return-delay allowance**,
the maximum deviation from the sample median, one full 0.864 ms Julian quantum,
and the 0.1 ms host error ceiling. The return-delay allowance is not a measured
or guaranteed bound; stable asymmetric delay cannot be identified from this
protocol. Direct PPS input to the mount remains the more accurate method.

Corrections use `:NUtim` in whole milliseconds, only outside the uncertainty plus
a half-millisecond rounding allowance. The driver rechecks host quality and
mount state before each correction, then verifies with new samples. At most three
corrections are attempted, with a 15-second operation context (an in-flight wire
command can additionally take its normal timeout). Offsets outside ±999 ms require
an approximate absolute time set first; there is no automatic coarse fallback.
The driver's polling and absolute time setters are serialized with this operation.

The JSON result contains `status` (`within_measurement_uncertainty` or
`not_converged`), `offset_ms` (positive means mount ahead), `uncertainty_ms`,
`round_trip_ms` (the median-offset sample), `corrections_ms`, and `host` accuracy diagnostics.
`method` is `receipt_estimate`; `return_delay_allowance_ms` exposes the assumption. `SetEnvironment`
returns this under `clock_sync`. A failed verification reports an error naming
already-applied nudges; it does not claim successful synchronization. Other
SetEnvironment fields may already have been applied when synchronization fails.
The reported uncertainty is conditional on the assumed return-delay allowance;
it is not a PPS-level accuracy guarantee for the mount.

The current source build uses the sibling `../lx200` checkout for `TimedGet`.
Publish that library addition and update the module version before distributing
this driver independently of the workspace.

## Automatic environment sources

Each source is optional and disabled unless configured. Example device config:

```json
{
  "driver": "tenmicron",
  "addr": "192.168.3.10:3490",
  "observingConditions": {
    "addr": "127.0.0.1:11304",
    "device": 0,
    "maxAgeSeconds": 30
  },
  "gpsd": "127.0.0.1:2947",
  "hostClockSync": true
}
```

Replace the ObservingConditions address/port and device number with your weather
service. This is its Alpaca HTTP endpoint, not an I2C address or mount TCP port.
The address also accepts an explicit `http://` or `https://` scheme.

Hardware acquisition does not initialize site or time. With `hostClockSync: true`,
the first client connection validates host-clock accuracy, sets UTC, and nudges
the clock. Repeated connect requests do not repeat this attempt; disconnecting
or a new hardware connection allows another attempt. The default is false.
With `gpsdSync: true` and a configured `gpsd` address, the first client connection
also applies GPS position. `gpsdSync` and `hostClockSync` independently default
to false when omitted. Repeated connect requests and failed attempts do not
trigger automatic retries. Resync can still explicitly apply GPS position.
The `Resync` action triggers this sequence:

1. If `hostClockSync` is enabled, validate host-clock quality and set approximate UTC.
2. If `gpsd` is configured, fetch and apply one fresh 3D location.
3. If `hostClockSync` is enabled, run the guarded clock-nudge routine.
Weather updates run independently every 30 seconds.

The full source synchronization runs when `Resync` is invoked with empty
parameters. With hostClockSync disabled, connections do not wait for synchronization.
The action attempts the configured steps once and returns SourceStatus diagnostics.
No automatic retries are scheduled. Independent stages still proceed after a
failure; a failed initial clock set skips nudging. Invoke Resync again to retry.

Weather is checked every 30 seconds independently. Standard Alpaca `connected`,
`pressure`, `temperature`, and per-sensor `timesincelastupdate` properties supply
the data. Both measurements must be valid and fresh (default maximum age 30 s).
Only values changed at the mount's 0.1 hPa / 0.1°C wire precision are sent; failed
writes are retried, and a hardware reconnect clears the last-sent cache.
These updates continue during tracking and do not turn on `DoesRefraction`.

GPS requires a complete 3D TPV with latitude, longitude, mean-sea-level altitude,
and a recent timestamp. The driver uses gpsd only; it does not open GPS hardware.
Leave `gpsd` absent to preserve a surveyed site or one managed by another client.
GPS time is not used to set the mount clock.

Host-clock initialization requires a synchronized host with reported error at most
100 µs and no competing mount GPS/PPS synchronization. The initial absolute UTC
set brings the mount within the fine-nudge range; the nudge routine then measures
and verifies its correction. Manual `SyncClock` still performs fine adjustment
only and now also refuses tracking, as well as slewing.

Use `SourceStatus` (empty parameters) for enabled sources, last attempts/successes,
errors, values and clock diagnostics. A clock result with `stage: set_utc` means
coarse UTC was set; the subsequent nudge result reports its verified accuracy.
The log reports synchronization step outcomes and
completion. Configuration changes require a driver restart.

When enabling these pulls, remove this mount from the weather driver's periodic
`feed` list so its push updates do not compete with the selected sources,
particularly by performing separate absolute-time writes. Explicit client
`SetEnvironment` and `SyncClock` actions remain available.

The generated schema exposes `observingConditionsAddr`,
`observingConditionsDevice`, and `observingConditionsMaxAgeSeconds` as flat
settings. For example:

```json
"observingConditionsAddr": "127.0.0.1:11304",
"observingConditionsDevice": 0,
"observingConditionsMaxAgeSeconds": 30
```

A blank address leaves weather pull disabled unless the legacy nested
`observingConditions` object is present. A nonblank flat address takes precedence
over that object. Maximum age zero uses the 30-second default.
