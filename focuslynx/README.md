# focuslynx

A standalone ASCOM **Alpaca Focuser** server for Optec FocusLynx / ThirdLynx focuser
hubs, built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`optec/focuslynx`](https://github.com/mikefsq/optec) library (USB-serial, **no vendor
SDK**). One process serves one or more focuser channels, each as its own Alpaca device.

The FocusLynx is a **two-channel** hub — one USB-serial connection drives both ports
`F1` and `F2`; the ThirdLynx is the single-channel (`F1`) variant. Each channel is an
independent absolute focuser.

## Build

```sh
go build .          # Go, CGO-free — cross-compiles to any target
```

### Linux permissions

The hub is a USB-serial device (`/dev/ttyUSB*`), in the `dialout` group:

```sh
sudo usermod -aG dialout "$USER"    # then re-login
```

## Run

Two ways to bind, **nickname** (preferred) or index + channel:

```sh
./focuslynx -nickname "OAG focuser"                    # one channel by its stored nickname
./focuslynx -nickname "Main focuser,Guide focuser"     # both channels → devices 0 and 1
./focuslynx -hub 0 -channel 2                           # or by enumeration index + channel
```

Each **nickname** becomes one Alpaca focuser device (0, 1, …); the hub and channel it
lives on are resolved over the protocol at connect time — so nickname binding is stable
regardless of plug order or platform, and lets one process serve both channels of a hub.
Without `-nickname`, a single device is bound by enumeration `-hub` index + `-channel`.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11122` | Alpaca HTTP port |
| `-nickname` | "" | comma-separated focuser nicknames — one Alpaca device per nickname, in order (stable, plug-order- and platform-independent) |
| `-hub` | `0` | hub enumeration index (used only when `-nickname` is empty) |
| `-channel` | `1` | focuser channel `1` (F1) or `2` (F2) (used only when `-nickname` is empty) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
