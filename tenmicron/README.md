# tenmicron

A standalone ASCOM **Alpaca Telescope** server for 10Micron GM-series mounts,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the
[`lx200/tenmicron`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # Go, no SDK
```

## Run

```sh
./tenmicron -addr 10.0.1.51:3492
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11200` | Alpaca HTTP port |
| `-addr` | "" (required) | mount TCP address `host:port` (10Micron uses 3490/3492) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
| `-lx200-port` | `0` | if non-zero, also serve an LX200 TCP server on this port (Stellarium/SkySafari) |

## Environment feed (refraction / site / time)

Another driver (an environment/GPS feeder) can push observing-site data into the
mount over the standard, stateless Alpaca port — no side channel:

- **Site latitude/longitude/elevation** and **UTC date** are standard ASCOM
  Telescope members — `PUT sitelatitude`, `sitelongitude`, `siteelevation`,
  `utcdate`.
- **Refraction pressure/temperature**, the **optical train**, and **dual-axis tracking**
  are not standard Telescope members, so they are exposed as ASCOM **Actions** (advertised
  in CamelCase, matched case-insensitively; see `GET supportedactions`). The read/write ones
  follow the fleet convention — **empty `Parameters` reads, a value writes**:

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

The driver **diffs** each field against the last applied (thresholded, so sensor noise /
GPS jitter doesn't churn the mount) and syncs the clock at most hourly; it returns
`{"applied":[…],"skipped":[…]}`. Reading `RefractionPressure`/`RefractionTemperature`
reflects a value set either way. Setting the refraction datums does not enable refraction —
set the standard `doesrefraction` member for that.

Optics can also be seeded at startup: `-aperture` (m), `-aperture-area` (m², default from
diameter), `-focal-length` (m).
