# asieaf

A standalone ASCOM Alpaca Focuser server for the ZWO EAF, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`goasi/eaf`](https://github.com/mikefsq/goasi) driver (USB-HID) — no ZWO SDK
runtime dependency. One process serves one focuser as Alpaca device 0 on its own port.

## Build

Linux and Windows builds do not require cgo. macOS uses IOKit through cgo.

```sh
# macOS (Apple silicon)
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o asieaf ./cmd/asieaf
# Linux / Raspberry Pi
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o asieaf ./cmd/asieaf
# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o asieaf.exe ./cmd/asieaf
```

### Linux permissions (udev)

`/dev/hidraw*` is root-only by default; install a rule so the logged-in user can
open the focuser:

```
# /etc/udev/rules.d/99-zwo-eaf.rules
KERNEL=="hidraw*", ATTRS{idVendor}=="03c3", MODE="0660", TAG+="uaccess"
```

Windows vendor-HID is user-accessible — no driver install needed.

## Run

```sh
./asieaf                      # serve on :11111, discovery=direct
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | currently ignored (EAF serial decode not yet implemented) |
| `-index` | `0` | enumeration index — the device selector |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
