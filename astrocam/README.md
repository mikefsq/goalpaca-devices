# astrocam

A standalone ASCOM **Alpaca Camera** server for ZWO ASI and PlayerOne cameras,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the **Go**
[`astrocam`](https://github.com/mikefsq/astrocam) camera library — the ZWO/PlayerOne
USB wire protocol implemented directly, with **no `libASICamera2` (ZWO SDK)**. One
process serves one *or more* cameras, each as its own Alpaca device (0, 1, …) on the
same port.

The USB transport is per-platform: **usbfs** on Linux and **WinUSB** on Windows (both
**cgo-free**), **IOKit** on macOS (cgo). So on Linux and Windows it cross-compiles to a
static binary with `CGO_ENABLED=0`.

> `astrocam` and [`asiccd`](../asiccd) are two routes to a ZWO camera — the Go
> protocol here vs. the official ZWO SDK — and share the default port `11111`. Run one
> or the other.

## Build

```sh
# macOS (Apple silicon) — cgo/IOKit
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o astrocam     .
# Linux / Raspberry Pi — Go, static
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o astrocam     .
# Windows — Go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o astrocam.exe .
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

Windows vendor-USB is user-accessible — no driver install needed.

## Run

```sh
./astrocam                                        # auto-enumerate every attached camera
./astrocam -serial 1a2b3c4d5e6f7080               # bind one camera by serial (recommended)
./astrocam -serial 1a2b3c4d5e6f7080,90a0b0c0d0e0  # two cameras → devices 0 and 1
```

The service starts even with **no camera attached** and acquires it when it appears —
bind by **serial** for start-before-plug and multi-camera setups. With no `-serial` and
no camera present, it still advertises device 0, which binds the first camera to appear.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | comma-separated factory serials (hex) — one Alpaca camera device per serial, in order (recommended); empty = auto-enumerate all attached |
| `-discovery` | `direct` | `direct` (self-answer on 32227) \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery (direct mode) |

`ASICAM_DEBUG=1` logs per-exposure arm/read/total timing.

## Capabilities

Standard ASCOM **ICameraV3** members, backed by the live sensor:

- **Exposure** — async `StartExposure` → `ImageReady` → `ImageArray`/`ImageBytes`, with
  `AbortExposure`/`StopExposure`. `ExposureResolution` is 1 µs; `ExposureMin`/`Max` come
  from the sensor.
- **Frame** — full-frame `ROI` (`StartX`/`Y`, `NumX`/`Y`) and symmetric binning up to the
  sensor's advertised max.
- **Readout mode** — `RAW16` (default) and `RAW8`; `MaxADU` follows (65535 / 255).
- **Gain / Offset** — over the sensor's live ranges.
- **Cooling (cooled models)** — `CoolerOn`, `SetCCDTemperature` (setpoint), `CCDTemperature`,
  `CoolerPower`. The driver owns the TEC for the process lifetime, so a client *disconnect*
  is a logical no-op and never resets cooling.
- **Guiding (ST4 models)** — `PulseGuide` / `IsPulseGuiding`.
- **Color** — `SensorType` and Bayer offsets are reported for OSC sensors.

The logical Alpaca **Connected** flag tracks hardware presence: one process owns the
camera for its lifetime, with an acquire → monitor → re-acquire loop that survives
unplug/replug (and a driver-confirmed device wedge) without dropping the Alpaca endpoint.

### Device Actions

Beyond the standard members, two device-specific Actions (advertised in CamelCase, matched
case-insensitively; see `GET supportedactions`):

| Action | Parameters | Effect |
|---|---|---|
| `VideoMode` | empty reads the current `true`/`false`; `on`/`off` (also `true`/`1`/`start`, `false`/`0`/`stop`) sets it | toggles continuous free-run streaming — for constant-exposure guiding at ~2× the single-shot rate. Frames still flow over the standard `ImageArray` path; only the acquisition engine changes. |
| `FpsPercent` | empty to query, or an integer `40..100` to set | the FPS-percent / bandwidth-overload throttle the readout HMAX/line-time math derates by. Lower = slower readout (larger HMAX) to fit a constrained USB link; the query returns the live value (link-dependent default — 100 on USB3, 40 on USB2 — until set). |

Factory **hot-pixel correction** (the per-unit defect map from SPI flash, applied to
full-frame RAW16) is available via `SetFixDefects` — off by default; the fleet enables
it per-camera with the `"fixdefects": true` config field (see [`../fleet`](../fleet)).

## Testing

End-to-end tests drive the **full stack** — Alpaca HTTP → server → this driver →
`astrocam` → a stub USB transport (a synthetic frame source) — fake transport, or real-hardware.

```sh
go test ./...                                                            # stub transport (no hardware, CI)
ASICAM_HARDWARE=1 ASICAM_SERIAL=<hex> go test -run TestAlpacaHardware -v ./...  # real camera attached
```
