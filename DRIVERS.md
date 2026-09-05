# Writing a driver

Each directory is a separate Go module with an importable driver package and
a standalone command under `cmd/<name>`. The package implements the relevant
[goalpaca device interface](https://github.com/mikefsq/goalpaca/blob/main/DRIVERS.md)
and registers a constructor in `hurd.go`.

Use [asiefw](asiefw) as a small example with live settings, or
[tenmicron](tenmicron) for a telescope backed by a protocol library.

## Registration and configuration

Register a `registry.Driver` with a unique `Name`, device `Type`, description,
config example, and `New` function. Provide `Config` when the driver has
settings. Decode the entry with `spec.Decode` so unknown driver keys are
rejected. Apply `spec.Name` when supplied.

`New` must validate and construct without opening hardware. Both `-check`
and device reload call it. Keep device identity stable across reconnects;
prefer a hardware serial to an enumeration index when available.

See [SETUP_FORMS.md](SETUP_FORMS.md) for config tags and live settings.

## Hardware lifecycle

Implement `server.Hardware`: start acquisition in `Open`, and stop background
work and release handles in `Close`. A reload closes the old instance before
opening its replacement. Wait for the old acquisition loop to stop so it
cannot reclaim the hardware.

Keep hardware ownership separate from Alpaca connection state. A client
disconnect must not release a power board or reset camera cooling. Guard
shared handles and cached state against concurrent requests. Document lock
requirements and protocol quirks where they affect the implementation.

Report invalid values, unavailable hardware, and unsupported operations using
goalpaca's Alpaca errors. Long operations should return promptly and expose
completion through the appropriate device property.

## Standalone command

The usual entry point imports the driver and calls `devicemain.Run`:

```go
package main

import (
    _ "example.com/mydriver"
    "github.com/mikefsq/goalpaca/devicemain"
)

func main() { devicemain.Run("mydriver") }
```

This supplies config loading, flags, setup forms, persistence, discovery, and
reload. Use `RunWith` for additional utility flags; see
[ptpcam/cmd/ptpcam](ptpcam/cmd/ptpcam).

Use platform file suffixes or build tags for platform-specific registration.
SDK drivers must document their build and runtime libraries.

## Build and test

Add the module to the appropriate root Makefile list. Modules producing more
than one binary also need a `moduledir` mapping. Packageable drivers are
listed separately in [build/build-deb](build/build-deb).

Run checks from the module directory:

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
go run ./cmd/mydriver -schema commented
go run ./cmd/mydriver -config device.json -check
```

Use fake transports to test acquisition, reconnects, shutdown, validation,
and hardware-specific behavior through Alpaca HTTP. Keep hardware tests
opt-in and document their environment variables and physical effects.
Do not assume the shared simulator's conformance tests suit every device.
