# goalpaca-devices

Standalone **ASCOM Alpaca** driver devices — each a small server that exposes a
piece of astronomy hardware over the Alpaca (REST/JSON + UDP discovery) protocol,
built on the [goalpaca](https://github.com/mikefsq/goalpaca) server library.

Every driver is its **own Go module and binary**: one process serves one device as
Alpaca device 0 on its own port. Most of these drivers are **Go with no vendor SDK** —
the device protocols were implemented directly (USB-HID, USB-serial, or the
reverse-engineered ZWO/PlayerOne camera wire protocol). Only the `goasi`-based ZWO
drivers are cgo and need the proprietary ZWO ASI SDK.

Each vendor-free driver module also **registers itself** with
[`goalpaca/registry`](https://github.com/mikefsq/goalpaca) (its `hurd.go` file:
name, ASCOM type, config example, factory), so the
[alpacahurd](https://github.com/mikefsq/alpacahurd) composed server can compile
it in with one `hurd.conf` line — standalone binary and herd membership from
the same module.

## Telescopes

LX200-family mounts, built on [`lx200`](https://github.com/mikefsq/lx200). Go, no SDK.

| Dir | Mount | Connect | Port |
|---|---|---|---|
| `tenmicron` | 10Micron GM-series | TCP | 11200 |
| `asiam5` | ZWO AM3/AM5/AM5N/AM7 | USB-serial or WiFi/TCP | 11201 |
| `rst` | Rainbow Astro RST-135/300 | USB-serial | 11202 |
| `onstep` | OnStep / OnStepX | USB-serial or WiFi/TCP | 11203 |

## Cameras

| Dir | Camera | Backend | Port |
|---|---|---|---|
| `astrocam` | ZWO ASI / PlayerOne | **Go** over [`astrocam`](https://github.com/mikefsq/astrocam) — no SDK | 11111 |
| `asiccd` | ZWO ASI | cgo, ZWO **ASICamera2** SDK (via `goasi`) | 11111 |

`astrocam` and `asiccd` are two routes to a ZWO camera (Go vs the official SDK);
run one or the other (they share the default port).

## Focusers

| Dir | Focuser | Backend | Port |
|---|---|---|---|
| `oasisfoc` | Astroasis Oasis | **Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) | 11120 |
| `focuscube` | Pegasus FocusCube / DMFC | **Go**, USB-serial ([`pegasus-astro`](https://github.com/mikefsq/pegasus-astro)) | 11121 |
| `focuslynx` | Optec FocusLynx / ThirdLynx | **Go**, USB-serial ([`optec`](https://github.com/mikefsq/optec)) | 11122 |
| `asieaf` | ZWO EAF | **Go**, USB-HID ([`goasi/eaf`](https://github.com/mikefsq/goasi)) — no SDK | 11112 |

## Filter wheels

| Dir | Wheel | Backend | Port |
|---|---|---|---|
| `oasisfw` | Astroasis Oasis | **Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) | 11123 |
| `asiefw` | ZWO EFW | **Go**, USB-HID ([`goasi/efw`](https://github.com/mikefsq/goasi)) — no SDK | 11113 |

## Rotators

| Dir | Rotator | Backend | Port |
|---|---|---|---|
| `asicaa` | ZWO CAA (Camera Angle Adjuster) | cgo, ZWO **CAA** SDK (via `goasi`) | 11114 |

## Observing conditions

Weather / sky-quality sensors, exposed as ASCOM **ObservingConditions**. Go, no SDK.

| Dir | Device | Backend | Port |
|---|---|---|---|
| `unihedron` | Unihedron SQM (sky quality meter) | **Go**, USB-serial ([`unihedron`](https://github.com/mikefsq/unihedron)) | 11124 |
| `mgpbox` | Astromi.ch MGPBox (GPS + weather + dew heater) | **Go**, USB-serial ([`astromi.ch`](https://github.com/mikefsq/astromi.ch)) | 11125 |

The MGPBox can also feed its GPS + weather into a `tenmicron` mount (site coordinates,
clock, and refraction pressure/temperature) via the `mountAddr` config field.

## The herd — one process, one config

Rather than launch each driver by hand,
**[alpacahurd](https://github.com/mikefsq/alpacahurd)** (its own repo) runs every
enabled device in a single process from one JSON config — each on its own Alpaca
port, behind a single UDP-32227 discovery responder, with per-device acquire/
reconnect so it survives an empty bus and unplug/replug. Which drivers are
compiled in is chosen at build time in its `hurd.conf`; the drivers here are the
default set.

It also serves two **optional native front-ends** over the same device objects (no
`indiserver`, no translation shims — they are siblings of the Alpaca server, sharing
the one source-of-truth device): **INDI** for PHD2 and **LX200** for
Stellarium / SkySafari. It ships `sim-*` drivers (one per ASCOM type) for client
development with no hardware, and installs as a systemd (Linux) or launchd (macOS)
service. See the [alpacahurd README](https://github.com/mikefsq/alpacahurd).

The herd's predecessor, `astrofleet` (in [`fleet/`](fleet/README.md)), is
deprecated and will be removed once alpacahurd is validated on deployed hardware.

## Build

From this directory the `Makefile` builds into `./bin`:

```sh
make                # every Go driver + astrofleet
make tenmicron      # one driver
make astrofleet     # the deprecated aggregator (superseded by alpacahurd)
make sdk            # the cgo ZWO-SDK drivers (asiccd, asicaa) — needs the ZWO lib
make sim            # run all simulated Alpaca devices (no hardware)
```

Or build any module in place: `cd tenmicron && go build .`, then e.g.
`./tenmicron -addr 10.0.1.51:3492`. The Go drivers (telescopes, `astrocam`,
`oasisfoc`/`oasisfw`, `focuslynx`, `focuscube`, `unihedron`, `mgpbox`, `asieaf`, `asiefw`)
need **no vendor SDK**; on Linux and Windows they build with `CGO_ENABLED=0` (the USB-HID
and `astrocam` USB backends use cgo only on macOS). The `goasi`-**SDK** drivers (`asiccd`,
`asicaa`) are cgo and require the ZWO ASI SDK runtime — see each driver's
`README.md` and the `goasi` README.

See each driver's `README.md` for its flags and behavior.

## Dependencies

- [`goalpaca`](https://github.com/mikefsq/goalpaca) — the Alpaca server framework (all drivers)
- [`lx200`](https://github.com/mikefsq/lx200) — mount protocol libraries (telescopes)
- [`goasi`](https://github.com/mikefsq/goasi) — ZWO camera/CAA SDK bindings (cgo: `asiccd`/`asicaa`) **and** pure-Go USB-HID drivers (`efw`, `eaf`)
- Go device libraries: [`astrocam`](https://github.com/mikefsq/astrocam) (ZWO/PlayerOne camera),
  [`oasis-astro`](https://github.com/mikefsq/oasis-astro), [`optec`](https://github.com/mikefsq/optec),
  [`pegasus-astro`](https://github.com/mikefsq/pegasus-astro),
  [`unihedron`](https://github.com/mikefsq/unihedron) (SQM sky-quality meter),
  [`astromi.ch`](https://github.com/mikefsq/astromi.ch) (MGPBox GPS/weather)

## License

MIT — see [LICENSE](LICENSE).
