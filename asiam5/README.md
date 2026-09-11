# asiam5

A standalone ASCOM Alpaca Telescope server for ZWO AM-series harmonic mounts
(AM3 / AM5 / AM5N / AM7), built on [`goalpaca`](https://github.com/mikefsq/goalpaca)
and the [`lx200/am5`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build -o asiam5 ./cmd/asiam5
```

### Linux permissions

For automatic USB discovery or binding by `-serial`, the mount's USB-serial adapter (`/dev/ttyUSB*` or
`/dev/ttyACM*`) is in the `dialout` group. Add the service user to it (the WiFi/TCP
`-addr` path needs no special permissions):

```sh
sudo usermod -aG dialout "$USER"    # then re-login
```

## Run

```sh
./asiam5                                # discover an attached AM-series USB mount
./asiam5 -serial /dev/tty.usbserial-XXXX   # USB-serial
./asiam5 -addr 192.168.4.1:4030            # WiFi/TCP
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | Optional USB serial number or device path |
| `-addr` | "" | WiFi/TCP address `host:port` (takes precedence over `-serial`) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

With neither selector set, the driver discovers the first responding AM-series
USB mount. It can start before the mount is attached and retries discovery until
one is available. Set `-serial` to select a specific mount when several are
attached, or `-addr` for a network connection. Configuration checks do not scan
for hardware.

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
