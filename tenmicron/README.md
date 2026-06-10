# tenmicron

A standalone ASCOM **Alpaca Telescope** server for 10Micron GM-series mounts,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the
[`lx200/tenmicron`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # pure Go, no SDK
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

## Environment feed (refraction / site / time)

Another driver (an environment/GPS feeder) can push observing-site data into the
mount over the standard, stateless Alpaca port — no side channel:

- **Site latitude/longitude/elevation** and **UTC date** are standard ASCOM
  Telescope members — `PUT sitelatitude`, `sitelongitude`, `siteelevation`,
  `utcdate`.
- **Refraction pressure/temperature** are not standard Telescope members, so they
  are exposed as ASCOM **Actions** (see `GET supportedactions`):

| Action | Parameters | Effect |
|---|---|---|
| `setrefractionpressure` | hPa (e.g. `1013.2`) | `:SRPRS` |
| `setrefractiontemperature` | °C (e.g. `-5.0`) | `:SRTMP` |
| `setenvironment` | JSON (all fields optional) | applies each present field |

`setenvironment` is a one-call convenience taking any subset:

```
PUT /api/v1/telescope/0/action
Action=setenvironment&Parameters={"pressure_hpa":1013.2,"temperature_c":-3.0,
  "latitude":47.3,"longitude":8.5,"elevation_m":540,"time":"2026-06-02T21:00:00Z"}
```

Only present fields are pushed (the feeder decides "as needed", since the port is
stateless). Setting the refraction datums does not enable refraction — set the
standard `doesrefraction` member for that. Returns `{"applied":[…]}`.

Optics for client instrument profiles are set at startup: `-aperture` (m),
`-aperture-area` (m², default from diameter), `-focal-length` (m).
