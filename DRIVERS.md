# Writing a driver

Each driver is a package in the `github.com/mikefsq/goalpaca-devices` module,
with a standalone command in `cmd/<name>` and a `Makefile` for building and
installing that one driver. The package implements the relevant
[goalpaca device interface](https://github.com/mikefsq/goalpaca/blob/main/DRIVERS.md)
and registers itself with the goalpaca registry in `hurd.go`.

[asiefw](asiefw) is a small example with live settings and a hardware scan.
[tenmicron](tenmicron) is a telescope driver backed by a protocol library.

## Registration

`hurd.go` registers a `registry.Driver` from an `init` function. The fields are:

| Field | Purpose |
|---|---|
| `Name` | unique driver name, also the binary name and the `driver` key in a device file |
| `Type` | Alpaca device type |
| `Description` | one line shown in alpacahurd's driver list |
| `ConfigExample` | a minimal device file entry |
| `Config` | returns a pointer to the zero-valued config struct. Its `json` and `alpaca` tags define the setup form and `-schema` output. |
| `New` | builds the device from a `registry.Spec` |
| `Identity` | config keys that address one device uniquely, most stable first, such as `{"serial", "index"}` or `{"addr"}` |
| `Scan` | lists attached devices for `-discover` and alpacahurd's Add device page. Nil for a device reached by address. |
| `MultiKey` | optional name of an array of devices in one entry |
| `FrontEnd` | optional protocol front-end started beside the Alpaca device, such as an LX200 server |

`New` decodes the entry with `spec.Decode`, which rejects unknown keys, and
applies `spec.Name` when one is set. `New` validates and constructs the device
without opening hardware, because `-check` and a device reload both call it.

`Scan` touches the bus, so it opens only what reading the identity requires
and releases it before returning. A device that another process holds is still
listed, with the values readable without taking it. `registry.CheckIdentity`
in a test confirms that every `Identity` key is a field of `Config`.

A device keeps its identity across reconnects. A hardware serial identifies a
device better than an enumeration index, where the hardware has one.
[SETUP_FORMS.md](SETUP_FORMS.md) describes the config tags and live settings.

## Hardware lifecycle

The device implements `server.Hardware`. `Open` starts acquisition, and `Close`
stops background work and releases handles. A reload closes the old instance
before opening its replacement. The old acquisition loop must stop before the
new instance opens, so it cannot reclaim the hardware.

Hardware ownership is separate from the Alpaca connection state. A client
disconnect does not release a power board or reset camera cooling. Shared
handles and cached state need guarding against concurrent requests. Comments
record lock requirements and protocol quirks where they affect the code.

Invalid values, unavailable hardware and unsupported operations return
goalpaca's Alpaca errors. A long operation returns promptly and reports
completion through the matching device property.

## Standalone command

The usual entry point imports the driver and calls `devicemain.Run`:

```go
package main

import (
    _ "github.com/mikefsq/goalpaca-devices/mydriver"
    "github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("mydriver") }
```

`devicemain` supplies config loading, the management flags (`-schema`,
`-discover`, `-check`), setup forms, persisted settings, Alpaca discovery and
reload. `RunWith` adds utility flags, as in [ptpcam/cmd/ptpcam](ptpcam/cmd/ptpcam).

Platform file suffixes or build tags select platform-specific registration.
An SDK driver's README names its build and runtime libraries.

## Build and test

A new driver needs four entries:

| File | Entry |
|---|---|
| `<name>/Makefile` | a copy of an existing driver's Makefile with `NAME` set |
| root `Makefile` | the name in `DRIVERS`, or in `PI_DRIVERS` for Raspberry Pi hardware |
| root `Makefile`, `driverdir` | only for a package that builds more than one binary, as smpro does |
| [build/build-deb](build/build-deb) | the name in `DRIVERS` for a Debian package. `MODULE_DIRS`, `ALIASES` and `ARM64_ONLY` cover multi-binary packages, extra driver names and Pi-only drivers. |

The checks run from the repository root:

```sh
go test ./mydriver/...
go vet ./mydriver/...
go run ./mydriver/cmd/mydriver -schema commented
go run ./mydriver/cmd/mydriver -discover
go run ./mydriver/cmd/mydriver -config device.json -check
make test vet
```

`make test` includes `internal/contract`, which finds every `cmd/*/main.go`,
builds it, and checks its `-schema` and management flags. A new driver is
covered there without further changes.

Driver tests use fake transports to cover acquisition, reconnects, shutdown,
validation and hardware-specific behavior through Alpaca HTTP. Hardware tests
are opt-in, and each one documents its environment variables and physical
effects. The simulator's conformance tests in `sim` do not suit every device.
