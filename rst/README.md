# rst

A standalone ASCOM **Alpaca Telescope** server for Rainbow Astro RST harmonic
mounts (RST-135 / RST-300), built on [`goalpaca`](https://github.com/mikefsq/goalpaca)
and the [`lx200/rst`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # pure Go, no SDK
```

## Run

```sh
./rst                                # auto-detect the first RST (FTDI 0403:6001)
./rst -serial /dev/tty.usbserial-XXXX
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11202` | Alpaca HTTP port |
| `-serial` | "" | USB-serial port; empty = auto-detect the first RST |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
