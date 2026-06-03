# tenmicron

A standalone ASCOM **Alpaca Telescope** server for 10Micron GM-series mounts,
built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and the
[`lx200/tenmicron`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # pure Go, no SDK
```

## Run

```sh
./tenmicron -addr 10.0.1.51:3492
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11200` | Alpaca HTTP port |
| `-addr` | "" (required) | mount TCP address `host:port` (10Micron uses 3490/3492) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
