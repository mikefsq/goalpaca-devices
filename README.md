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

`make install` (or `make install` in a driver directory) records each binary in
`/etc/alpacahurd/drivers.conf`, alongside the existing disabled seed
in `devices.d/`. The registry contains one absolute executable path per line.
Updating the registry does not execute the driver or create runtime state. Upgrades preserve existing device configurations.
Uninstall removes the binary's registry entry and keeps device configurations.
Debian packages maintain the same registry, using their `/usr/bin` paths.

For a binary installed before this registry was introduced, register it without
rebuilding or starting it:

```sh
sudo sh build/register-driver /usr/local/bin/asiam5 /etc/alpacahurd/drivers.conf
```

The Add device page in alpacahurd reads this catalogue on each visit and can
create additional disabled instances from a binary's `-schema commented` output.
Legacy launchers without schema support can be recorded, but cannot generate
configuration through that page. On macOS, per-driver installs place the registry
beside the configured `devices.d` directory; `REGISTRYFILE` overrides that location.

## Discover hardware

Run `<driver> -discover` to list detected hardware as JSON and exit without
loading configuration, constructing a device, starting an Alpaca server, or
writing state. This is distinct from `-discovery`, which controls Alpaca network
advertising. Explicit `-discover` scans regardless of any supplied config pin.

The output includes `driver`, `supported`, ordered `identity` configuration keys,
and `devices`, each with a `label` and `values` containing usable configuration
selectors. Unknown selectors are omitted. A busy device may be listed without a
serial; discovery must not interrupt the process using it.

No devices found returns an empty `devices` array. Drivers without a scanner
return `supported: false` and an empty array. Scan failures include `error` in the
JSON and exit nonzero; diagnostics go to stderr. Scans receive a ten-second
context deadline, which each scanner must honor.

All launchers using `goalpaca/devicemain` inherit this flag. The custom Astrocam
and simulator launchers also support it (the simulator has no hardware to scan).
Rebuild installed binaries to obtain the new flag.

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
