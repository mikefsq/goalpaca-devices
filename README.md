# goalpaca-devices

ASCOM Alpaca drivers for astronomy hardware, built on
[goalpaca](https://github.com/mikefsq/goalpaca). Run a driver on the computer
connected to the device, then connect an Alpaca client over the network.
Use [alpacahurd](https://github.com/mikefsq/alpacahurd) to manage several drivers.

## Drivers

| Type | Driver | Hardware |
|---|---|---|
| Telescope | [tenmicron](tenmicron/README.md) | 10Micron GM-series, TCP |
| Telescope | [asiam5](asiam5/README.md) | ZWO AM-series, serial or TCP |
| Telescope | [rst](rst/README.md) | Rainbow Astro RST, USB-serial |
| Telescope | [onstep](onstep/README.md) | OnStep / OnStepX, serial or TCP |
| Camera | [astrocam](astrocam/README.md) | USB astronomy cameras, no vendor SDK |
| Camera | [ptpcam](ptpcam/README.md) | Fujifilm and Sony USB PTP cameras |
| Camera | [asiccd](asiccd/README.md) | ZWO ASI cameras, ZWO SDK |
| Focuser | [asieaf](asieaf/README.md) | ZWO EAF |
| Focuser | [focuscube](focuscube/README.md) | Pegasus FocusCube / DMFC |
| Focuser | [focuslynx](focuslynx/README.md) | Optec FocusLynx / ThirdLynx |
| Focuser | [oasisfoc](oasisfoc/README.md) | Astroasis Oasis |
| Filter wheel | [asiefw](asiefw/README.md) | ZWO EFW |
| Filter wheel | [oasisfw](oasisfw/README.md) | Astroasis Oasis |
| Rotator | [asicaa](asicaa/README.md) | ZWO CAA, ZWO SDK |
| Observing conditions | [mgpbox](mgpbox/README.md) | Astromi.ch MGPBox weather and GPS |
| Observing conditions | [unihedron](unihedron/README.md) | Unihedron SQM |
| Switch | [asiair](asiair/README.md) | ZWO ASIAIR power board; runs on its Pi |
| Switch / Focuser | [smpro](smpro/README.md) | StellarMate SM Pro; runs on its Pi |
| Simulator | [sim](sim/README.md) | Coupled telescope and guide camera |

## Install and build

Debian packages and installation instructions are in the
[APT archive](https://mikefsq.github.io/apt/). See
[releases](https://github.com/mikefsq/goalpaca-devices/releases) for release assets.

For a source build, install Go 1.25 or later. Each driver is a separate Go module.
From this directory:

```sh
make tenmicron      # build bin/tenmicron
make                # build drivers that do not need a vendor SDK
make pi             # build Raspberry Pi drivers for Linux arm64
make help
```

The SDK drivers require cgo and their ZWO libraries; see their READMEs.
Some macOS USB transports also require cgo and Apple's command-line tools.

## Run

```sh
./bin/tenmicron -addr 192.168.1.50:3492 -port 11200
```

Most drivers default to HTTP port `11111`; choose a different `-port` for
each server on the same machine. The guide simulator defaults to `11110`.
Open `http://localhost:11200/setup` for the example above.

Drivers answer Alpaca discovery on UDP port `32227`. When using a discovery
proxy, select `-discovery register -discovery-server host:32227`.
Use `-discovery off` to connect by address only.

## Configuration

Most drivers accept a JSONC device file, with `//` comments:

```sh
./bin/tenmicron -schema commented > mount.json
./bin/tenmicron -config mount.json -check
./bin/tenmicron -config mount.json
```

Edit the generated file before checking it. `-check` validates configuration
without opening hardware. Flags override file values; `-help` lists the
available flags. The simulator uses command-line flags only.

The setup page allows changes to live settings that are not fixed by the
file or flags. Change hardware selectors in the file and reload or restart
the driver. Use stable serial identifiers where supported; the EAF currently
selects by enumeration index only.

## Writing a driver

See [DRIVERS.md](DRIVERS.md) for registration, hardware lifecycle, and testing,
and [SETUP_FORMS.md](SETUP_FORMS.md) for browser settings.

## License

[MIT](LICENSE).
