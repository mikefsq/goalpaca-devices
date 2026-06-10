# asiefw

A standalone ASCOM **Alpaca FilterWheel** server for the ZWO EFW, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and the pure-Go
[`goasi/efw`](https://github.com/mikefsq/goasi) driver — **no ZWO SDK runtime
dependency**. One process serves one wheel as Alpaca device 0 on its own port.

## Build

Pure Go on Linux/Windows; macOS uses IOKit (cgo, on by default). Cross-compiles
from any host to a static binary.

```sh
# macOS (Apple silicon)
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o asiefw     .
# Linux / Raspberry Pi
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o asiefw     .
# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o asiefw.exe .
```

### Linux permissions (udev)

`/dev/hidraw*` is root-only by default; install a rule so the service user can
open the wheel:

```
# /etc/udev/rules.d/99-zwo-efw.rules
KERNEL=="hidraw*", ATTRS{idVendor}=="03c3", MODE="0660", TAG+="uaccess"
```

Windows vendor-HID is user-accessible — no driver install needed.

## Run

```sh
./asiefw                              # serve on :11113, discovery=direct
./asiefw -serial 1f2120703dcef2b1     # bind a specific wheel (recommended)
./asiefw -unidirectional              # repeatable filter seating (vs shortest path)
```

The service starts even with no wheel attached and acquires it when it appears —
bind by **serial** for start-before-plug and multi-device setups.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11113` | Alpaca HTTP port |
| `-serial` | "" | bind the wheel with this factory serial (hex); recommended |
| `-wheel` | `0` | enumeration index (used only when `-serial` is empty) |
| `-unidirectional` | false | always rotate the same way (repeatable seating) vs bidirectional shortest path |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

## Testing

End-to-end tests drive the **full stack** — Alpaca HTTP → server → this driver →
`goasi/efw` → transport — with no hardware (a fake transport), plus an optional
real-hardware run.

```sh
go test -race ./...                                            # fake transport (no hardware, CI)
EFW_HARDWARE=1 go test -race -run TestAlpacaHardware -v ./...  # real wheel (physically rotates it)
```

