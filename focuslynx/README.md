# focuslynx

A standalone ASCOM Alpaca Focuser server for Optec FocusLynx / ThirdLynx focuser
hubs, built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`optec/focuslynx`](https://github.com/mikefsq/optec) library (USB-serial, no vendor
SDK). One process serves one focuser channel as device 0.

The FocusLynx is a two-channel hub — one USB-serial connection drives both ports
`F1` and `F2`; the ThirdLynx is the single-channel (`F1`) variant. Each channel is an
independent absolute focuser.

## Build

```sh
go build -o focuslynx ./cmd/focuslynx
```

### Linux permissions

The hub is a USB-serial device (`/dev/ttyUSB*`), in the `dialout` group:

```sh
sudo usermod -aG dialout "$USER"    # then re-login
```

## Run

Select a channel by its stored nickname, or by hub index and channel:

```sh
./focuslynx -nickname "OAG focuser"
./focuslynx -index 0 -channel 2
```

Run a separate instance on another port for the other channel. Nicknames
are resolved when connecting and do not depend on USB enumeration order.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-nickname` | "" | stored nickname of one focuser channel |
| `-index` | `0` | hub enumeration index (used only when `-nickname` is empty) |
| `-channel` | `1` | focuser channel `1` (F1) or `2` (F2) (used only when `-nickname` is empty) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
