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
| Camera | [astrocam](astrocam/README.md) | USB astronomy cameras like the ASI6200 |
| Camera | [ptpcam](ptpcam/README.md) | Fujifilm and Sony USB PTP cameras |
| Camera | [polemaster](polemaster/README.md) | QHY PoleMaster |
| Focuser | [asieaf](asieaf/README.md) | ZWO EAF |
| Focuser | [focuscube](focuscube/README.md) | Pegasus FocusCube / DMFC |
| Focuser | [focuslynx](focuslynx/README.md) | Optec FocusLynx / ThirdLynx |
| Focuser | [oasisfoc](oasisfoc/README.md) | Astroasis Oasis |
| Filter wheel | [asiefw](asiefw/README.md) | ZWO EFW |
| Filter wheel | [oasisfw](oasisfw/README.md) | Astroasis Oasis |
| Observing conditions | [mgpbox](mgpbox/README.md) | Astromi.ch MGPBox weather and GPS |
| Observing conditions | [unihedron](unihedron/README.md) | Unihedron SQM |
| Switch | [asiair](asiair/README.md) | ZWO ASIAIR power board; runs on its Pi |
| Switch / Focuser | [smpro](smpro/README.md) | StellarMate SM Pro; runs on its Pi |
| Switch | [scopehub](scopehub/README.md) | scope_hub power and USB hub |
| Observing conditions | [pi_mount_gps](pi_mount_gps/README.md) | Pi GPS/weather board (SHT45, BMP580, u-blox); gpsd with I²C fallback |
| Simulator | [sim](sim/README.md) | Coupled telescope and guide camera |

## Install and build

Release packages target **Debian Trixie**, on **amd64 and arm64**.
ASIAIR and SM Pro packages are arm64 only because they access Pi hardware. 

Debian packages and installation instructions are in the
[APT archive](https://mikefsq.github.io/apt/). See
[releases](https://github.com/mikefsq/goalpaca-devices/releases) for release assets.

For a source build, install Go 1.25 or later. All drivers are packages in one Go 
module, `github.com/mikefsq/goalpaca-devices`. The root `go.mod` records shared, 
released library dependencies. Individual executables and Debian packages 
remain separate.

From this directory:

```sh
make deps           # download the dependency versions recorded in go.mod
make                # build drivers that do not need a vendor SDK
make tenmicron      # build bin/tenmicron
make pi             # build Raspberry Pi drivers for Linux arm64
sudo make install   # copy bin/ to /usr/local/bin and register in drivers.conf
sudo make seed      # write a disabled devices.d template per installed driver
sudo make uninstall # remove the binaries; device files are kept
make help
```

Some macOS USB transports also require cgo and Apple's command-line tools.

`make install` (or `make install` in a driver directory) copies each binary and
records its absolute path in `drivers.conf`. `make seed` writes one disabled 
template per driver into `devices.d/`. These are written to `/etc/alpacahurd` 
on Linux and `/Library/Application Support/alpacahurd` on macOS.

## Management commands

Every packaged driver exposes the same management commands:

- `-schema json`: machine-readable configuration fields.
- `-schema commented`: a flat, disabled prototype with optional fields commented out.
- `-discover`: structured device identities, or `supported: false` when the driver cannot scan.
- `-config FILE -check`: validate configuration without connecting hardware.

### Discover hardware

Run `<driver> -discover` to list detected hardware as JSON and exit. The output 
includes `driver`, `supported`, ordered `identity` configuration keys, and `devices`, 
each with a `label` and `values` containing usable configuration
selectors. 

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

## Writing a driver

See [DRIVERS.md](DRIVERS.md) for registration, hardware lifecycle, and testing,
and [SETUP_FORMS.md](SETUP_FORMS.md) for browser settings.

## License

[AGPL-3.0-only](LICENSE).
