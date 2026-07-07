# oasisfoc

A standalone ASCOM **Alpaca Focuser** server for the Astroasis Oasis focuser, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`oasis-astro/oasisfoc`](https://github.com/mikefsq/oasis-astro) library (USB-HID, **no
vendor SDK**). One process serves one focuser as Alpaca device 0 on its own port.

It's an **absolute** focuser (encoder + `MoveTo`/`Position`). It also has a **manual
clutch**, so the reported position can change without a commanded move — the encoder
still tracks it, and the driver reports the live position. The travel limit
(`MaxStep`) and temperature are read from the device.

## Build

Go on Linux/Windows; macOS HID uses cgo (IOKit), on by default. Cross-compiles to
a static binary on Linux/Windows.

```sh
CGO_ENABLED=1 GOOS=darwin  GOARCH=arm64 go build -o oasisfoc     .   # macOS (IOKit)
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o oasisfoc     .   # Linux / Raspberry Pi
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o oasisfoc.exe .   # Windows
```

### Linux permissions (udev)

The Oasis is a USB-HID device (`/dev/hidraw*`, Astroasis VID `338f`), root-only by
default. Install a rule so the service user can open it:

```
# /etc/udev/rules.d/99-oasis.rules
KERNEL=="hidraw*", ATTRS{idVendor}=="338f", MODE="0660", TAG+="uaccess"
```

```sh
sudo udevadm control --reload && sudo udevadm trigger   # then replug the focuser
```

Windows vendor-HID is user-accessible — no driver install needed.

## Run

```sh
./oasisfoc -port 11120 -focuser 0
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11120` | Alpaca HTTP port |
| `-focuser` | `0` | Oasis focuser enumeration index |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
