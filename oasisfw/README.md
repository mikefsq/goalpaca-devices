# oasisfw

A standalone ASCOM **Alpaca FilterWheel** server for the Astroasis Oasis filter wheel,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`oasis-astro/oasisfw`](https://github.com/mikefsq/oasis-astro) library (USB-HID, **no
vendor SDK**). One process serves one wheel as Alpaca device 0 on its own port.

Positions are 0-based and report `-1` while moving (the ASCOM convention); slot
**names** and per-slot **focus offsets** are read from the wheel at connect.

## Build

Go on Linux/Windows; macOS HID uses cgo (IOKit), on by default. Cross-compiles to
a static binary on Linux/Windows.

```sh
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o oasisfw     .   # macOS (IOKit)
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o oasisfw     .   # Linux / Raspberry Pi
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o oasisfw.exe .   # Windows
```

### Linux permissions (udev)

The Oasis is a USB-HID device (`/dev/hidraw*`, Astroasis VID `338f`), root-only by
default. Install a rule so the service user can open it:

```
# /etc/udev/rules.d/99-oasis.rules
KERNEL=="hidraw*", ATTRS{idVendor}=="338f", MODE="0660", TAG+="uaccess"
```

```sh
sudo udevadm control --reload && sudo udevadm trigger   # then replug the wheel
```

Windows vendor-HID is user-accessible — no driver install needed.

## Run

```sh
./oasisfw -port 11123 -wheel 0
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11123` | Alpaca HTTP port |
| `-wheel` | `0` | Oasis filter wheel enumeration index |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

## Device Actions

Beyond ASCOM IFilterWheel, the Oasis exposes device-specific **Actions** (`PUT …/action`;
names advertised in CamelCase, matched case-insensitively; see `GET supportedactions`).
Config fields follow the fleet convention — **empty `Parameters` reads, a value writes**:

- **Config (read/write):** `Speed`, `Autorun`, `BluetoothOn`, `Turbo`, `FriendlyName`,
  `BluetoothName` (booleans take `1/0/true/false/on/off`).
- **Read-only:** `Serial`, `Model`, `HardwareVersion`, `FirmwareVersion`,
  `FirmwareBuildDate`, `ProtocolVersion`, `Temperature`, `TemperatureRaw`, `Slots`, `State`,
  `Config` (a value is rejected).
- **Per-slot** (the index is required both ways, so read + `SetX` stay separate):
  `SlotName`/`FocusOffset`/`Color` read with `Parameters=<slot>`; `SetSlotName`/
  `SetFocusOffset`/`SetColor` write with `Parameters=<slot>:<value>` (e.g. `1:Ha`, `0:00ff00`).
- **Maintenance:** `Calibrate`; `FactoryReset` requires `Parameters=confirm`.
