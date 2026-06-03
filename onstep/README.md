# onstep

A standalone ASCOM **Alpaca Telescope** server for OnStep / OnStepX controllers,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the
[`lx200/onstep`](https://github.com/mikefsq/lx200) protocol library. One process
serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # pure Go, no SDK
```

## Run

```sh
./onstep -serial /dev/tty.usbserial-XXXX   # USB-serial
./onstep -addr 192.168.0.1:9999            # WiFi/TCP
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11203` | Alpaca HTTP port |
| `-serial` | "" | USB-serial port |
| `-addr` | "" | WiFi/TCP address `host:port` (takes precedence over `-serial`) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

Give either `-serial` or `-addr`.
