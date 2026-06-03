# asiefw

A standalone ASCOM **Alpaca FilterWheel** server for the ZWO EFW, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and [`goasi/efw`](https://github.com/mikefsq/goasi).
One process serves one wheel as Alpaca device 0 on its own port.

## Build

```sh
CGO_ENABLED=1 go build .
```

Links `-lEFWFilter` from `/usr/local/lib`. The ZWO SDK is **not** bundled —
download it from ZWO and install `libEFWFilter` into `/usr/local/lib` (or build
with `CGO_LDFLAGS="-L/path/to/lib"`; on macOS rewrite the binary's path with
`install_name_tool`). See the [`goasi`](https://github.com/mikefsq/goasi) README.

## Run

```sh
./asiefw                      # serve on :11113, discovery=direct
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11113` | Alpaca HTTP port |
| `-serial` | "" | bind the wheel with this serial (hex); recommended |
| `-wheel` | `0` | enumeration index (used only when `-serial` is empty) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
