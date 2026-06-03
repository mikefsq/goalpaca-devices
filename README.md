# goalpaca_devices

Standalone **ASCOM Alpaca** driver devices — each a small server that exposes a
piece of astronomy hardware over the Alpaca (REST/JSON + UDP discovery) protocol,
built on the [goalpaca](https://github.com/mikefsq/goalpaca) server library.

Every driver is its **own Go module and binary**: one process serves one device
as Alpaca device 0 on its own port. Nothing is shared between drivers beyond the
published libraries they sit on.

## Telescopes

LX200-family mounts, built on [`lx200`](https://github.com/mikefsq/lx200) (the
low-level protocol lib). Pure Go — `go build .`, no SDK.

| Dir | Mount | Connect | Port |
|---|---|---|---|
| `tenmicron` | 10Micron GM-series | TCP | 11200 |
| `asiam5` | ZWO AM3/AM5/AM5N/AM7 | USB-serial or WiFi/TCP | 11201 |
| `rst` | Rainbow Astro RST-135/300 | USB-serial | 11202 |
| `onstep` | OnStep / OnStepX | USB-serial or WiFi/TCP | 11203 |

## ZWO ASI devices

ZWO cameras and accessories, built on [`goasi`](https://github.com/mikefsq/goasi)
(the SDK bindings). These are **cgo** and need the proprietary ZWO ASI SDK
runtime library installed (see each driver's README and the `goasi` README).

| Dir | Device | ZWO SDK |
|---|---|---|
| `asiccd` | Camera | ASICamera2 |
| `asieaf` | Focuser | EAF |
| `asicaa` | Rotator (Camera Angle Adjuster) | CAA |
| `asiefw` | Filter wheel | EFW |

## Build & run

Each driver is its own Go module, built with the `Makefile`:

```sh
make                  # build all eight drivers into ./bin
make tenmicron        # build just one (any driver name)
make clean            # remove ./bin
```

CGO is enabled for the ASI drivers automatically (and is harmless for the
pure-Go telescopes). To build a single driver in place instead:

```sh
cd tenmicron && go build .                # pure-Go telescope
cd asiccd && CGO_ENABLED=1 go build .     # ASI device (needs the ZWO lib)
./tenmicron -addr 10.0.1.51:3492          # then run it
```

See each driver's `README.md` for its flags and behavior. 

## Dependencies

- [`goalpaca`](https://github.com/mikefsq/goalpaca) — the Alpaca server framework (all drivers)
- [`lx200`](https://github.com/mikefsq/lx200) — mount protocol libraries (telescope drivers)
- [`goasi`](https://github.com/mikefsq/goasi) — ZWO SDK bindings (ASI drivers)

## License

MIT — see [LICENSE](LICENSE).
