# asieaf

A standalone ASCOM **Alpaca Focuser** server for the ZWO EAF, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and [`goasi/eaf`](https://github.com/mikefsq/goasi).
One process serves one focuser as Alpaca device 0 on its own port.

## Build

```sh
CGO_ENABLED=1 go build .
```

Links `-lEAFFocuser` from `/usr/local/lib`. The ZWO SDK is **not** bundled —
download it from ZWO and install `libEAFFocuser` into `/usr/local/lib` (or build
with `CGO_LDFLAGS="-L/path/to/lib"`; on macOS rewrite the binary's path with
`install_name_tool`). See the [`goasi`](https://github.com/mikefsq/goasi) README.
On Linux the EAF also needs `libsdbus-c++.so.2` and `libWrapperSdbus.so` from the
same SDK lib directory.

## Run

```sh
./asieaf                      # serve on :11112, discovery=direct
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11112` | Alpaca HTTP port |
| `-serial` | "" | bind the focuser with this serial (hex); recommended |
| `-focuser` | `0` | enumeration index (used only when `-serial` is empty) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |
