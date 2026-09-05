# astrocam

A standalone ASCOM Alpaca Camera server for various CMOS astrophotography cameras
(e.g. ASI6200, ASI174MM) built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`astrocam`](https://github.com/mikefsq/astrocam) camera library. One
process serves one *or more* cameras, each as its own Alpaca device (0, 1, …) on the
same port.

The USB transport is implemented per-platform: usbfs on Linux and WinUSB on Windows,
and IOKit on macOS.

## Build

```sh
# macOS (Apple silicon) — cgo/IOKit
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o astrocam ./cmd/astrocam
# Linux / Raspberry Pi — Go, static
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o astrocam ./cmd/astrocam
# Windows — Go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o astrocam.exe ./cmd/astrocam
```

### Linux permissions (udev)

The camera is driven over usbfs (`/dev/bus/usb/*`), and serial binding opens each
candidate to read its factory serial (ASI/POA cameras expose no USB serial-number
descriptor — it lives in flash, read via a vendor control transfer). Both need
read-write access to the device node. Install a rule for the vendors you use:

```
# /etc/udev/rules.d/99-astrocam.rules
SUBSYSTEM=="usb", ATTRS{idVendor}=="03c3", MODE="0660", TAG+="uaccess"   # ZWO ASI
SUBSYSTEM=="usb", ATTRS{idVendor}=="a0a0", MODE="0660", TAG+="uaccess"   # PlayerOne
```

```sh
sudo udevadm control --reload && sudo udevadm trigger   # then replug the camera
```

On Windows, the camera must be accessible through WinUSB.

## Run

```sh
./astrocam                                        # auto-enumerate every attached camera
./astrocam -serial 1a2b3c4d5e6f7080               # bind one camera by serial (recommended)
./astrocam -serial 1a2b3c4d5e6f7080,90a0b0c0d0e0  # two cameras → devices 0 and 1
```

The service starts even with no camera attached and acquires it when it appears —
bind by serial for start-before-plug and multi-camera setups. With no `-serial` and
no camera present, it still advertises device 0, which binds the first camera to appear.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | comma-separated factory serials (hex) — one Alpaca camera device per serial, in order (recommended); empty = auto-enumerate all attached |
| `-discovery` | `direct` | `direct` (self-answer on 32227) \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery (direct mode) |

`ASICAM_DEBUG=1` logs per-exposure arm/read/total timing.

## Capture and cooling

Readout modes include RAW16 and RAW8, plus sensor-specific modes when supported.
The driver trims ROI dimensions to hardware alignment requirements; read back
the resulting dimensions after configuring a subframe.

Cooling continues across client disconnects. The driver reacquires the camera
after unplugging or a recoverable device failure. `Connected` reports hardware
availability.

### Device Actions

These device-specific Actions are supported (see `GET supportedactions`):

| Action | Parameters | Effect |
|---|---|---|
| `VideoMode` | empty to read; `on` or `off` to set | enable continuous streaming |
| `FpsPercent` | empty to read; `40..100` to set | reduce readout bandwidth for a constrained USB link |
| `CoolerFault` | empty | read the cooling-loop error; enabling `CoolerOn` retries regulation |

Factory hot-pixel correction is on by default for full-frame RAW16. Set
`"fixdefects": false` in the config file to disable it.

## Testing

Tests use a simulated USB transport unless hardware testing is enabled.

```sh
go test ./...                                                            # stub transport (no hardware, CI)
ASICAM_HARDWARE=1 ASICAM_SERIAL=<hex> go test -run TestAlpacaHardware -v ./...  # real camera attached
```

Use `-config` for a JSONC device file; `-schema commented` generates a
sample with a `cameras` array. `-check` validates it without opening hardware.
