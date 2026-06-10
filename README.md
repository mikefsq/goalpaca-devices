# goalpaca-devices

Standalone **ASCOM Alpaca** driver devices — each a small server that exposes a
piece of astronomy hardware over the Alpaca (REST/JSON + UDP discovery) protocol,
built on the [goalpaca](https://github.com/mikefsq/goalpaca) server library.

Every driver is its **own Go module and binary**: one process serves one device as
Alpaca device 0 on its own port. Most of these drivers are **pure Go with no vendor SDK** —
the device protocols were implemented directly (USB-HID, USB-serial, or the
reverse-engineered ZWO/PlayerOne camera wire protocol). Only the `goasi`-based ZWO
drivers are cgo and need the proprietary ZWO ASI SDK.

## Telescopes

LX200-family mounts, built on [`lx200`](https://github.com/mikefsq/lx200). Pure Go, no SDK.

| Dir | Mount | Connect | Port |
|---|---|---|---|
| `tenmicron` | 10Micron GM-series | TCP | 11200 |
| `asiam5` | ZWO AM3/AM5/AM5N/AM7 | USB-serial or WiFi/TCP | 11201 |
| `rst` | Rainbow Astro RST-135/300 | USB-serial | 11202 |
| `onstep` | OnStep / OnStepX | USB-serial or WiFi/TCP | 11203 |

## Cameras

| Dir | Camera | Backend | Port |
|---|---|---|---|
| `astrocam` | ZWO ASI / PlayerOne | **pure Go** over [`astrocam`](https://github.com/mikefsq/astrocam) — no SDK | 11111 |
| `asiccd` | ZWO ASI | cgo, ZWO **ASICamera2** SDK (via `goasi`) | 11111 |

`astrocam` and `asiccd` are two routes to a ZWO camera (pure-Go vs the official SDK);
run one or the other (they share the default port).

## Focusers

| Dir | Focuser | Backend | Port |
|---|---|---|---|
| `oasisfoc` | Astroasis Oasis | **pure Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) | 11120 |
| `focuscube` | Pegasus FocusCube / DMFC | **pure Go**, USB-serial ([`pegasus-astro`](https://github.com/mikefsq/pegasus-astro)) | 11121 |
| `focuslynx` | Optec FocusLynx / ThirdLynx | **pure Go**, USB-serial ([`optec-astro`](https://github.com/mikefsq/optec-astro)) | 11122 |
| `asieaf` | ZWO EAF | cgo, ZWO **EAF** SDK (via `goasi`) | 11112 |

## Filter wheels

| Dir | Wheel | Backend | Port |
|---|---|---|---|
| `oasisfw` | Astroasis Oasis | **pure Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) | 11123 |
| `asiefw` | ZWO EFW | cgo, ZWO **EFW** SDK (via `goasi`) | 11113 |

## Rotators

| Dir | Rotator | Backend | Port |
|---|---|---|---|
| `asicaa` | ZWO CAA (Camera Angle Adjuster) | cgo, ZWO **CAA** SDK (via `goasi`) | 11114 |

## Build & run

Each driver is its own module — build any one in place:

```sh
cd astrocam  && CGO_ENABLED=0 go build ./cmd/astrocam   # pure-Go (cgo only for the macOS USB backend)
cd tenmicron && go build .                               # pure-Go telescope
cd asiccd    && CGO_ENABLED=1 go build .                 # ZWO SDK device (needs the ZWO lib)
./tenmicron -addr 10.0.1.51:3492                         # then run it
```

The pure-Go drivers (telescopes, `astrocam`, `oasisfoc`/`oasisfw`, `focuslynx`,
`focuscube`) need **no vendor SDK**; on Linux and Windows they build with
`CGO_ENABLED=0` (the USB-HID and `astrocam` USB backends use cgo only on macOS). The
`goasi`-based ZWO drivers (`asiccd`, `asieaf`, `asiefw`, `asicaa`) are cgo and require
the ZWO ASI SDK runtime — see each driver's `README.md` and the `goasi` README.

> Note: the `Makefile` currently builds only the `goasi`/telescope subset
> (`tenmicron asiam5 rst onstep asiccd asieaf asicaa asiefw`) into `./bin`; the pure-Go
> drivers build standalone with `go build` as above.

See each driver's `README.md` for its flags and behavior.

## Dependencies

- [`goalpaca`](https://github.com/mikefsq/goalpaca) — the Alpaca server framework (all drivers)
- [`lx200`](https://github.com/mikefsq/lx200) — mount protocol libraries (telescopes)
- [`goasi`](https://github.com/mikefsq/goasi) — ZWO SDK bindings (cgo ASI drivers)
- Pure-Go device libraries: [`astrocam`](https://github.com/mikefsq/astrocam) (ZWO/PlayerOne camera),
  [`oasis-astro`](https://github.com/mikefsq/oasis-astro), [`optec-astro`](https://github.com/mikefsq/optec-astro),
  [`pegasus-astro`](https://github.com/mikefsq/pegasus-astro)

## License

MIT — see [LICENSE](LICENSE).
