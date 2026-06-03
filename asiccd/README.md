# asiccd

A standalone ASCOM **Alpaca Camera** server for ZWO ASI cameras, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and [`goasi/ccd`](https://github.com/mikefsq/goasi).
One process serves one camera as Alpaca device 0 on its own port; run multiple
instances (different ports + `-serial`) for multiple cameras.

## Build

```sh
CGO_ENABLED=1 go build .
```

Links `-lASICamera2` from `/usr/local/lib`. The ZWO SDK is **not** bundled —
download it from ZWO and install `libASICamera2` into `/usr/local/lib` (or build
with `CGO_LDFLAGS="-L/path/to/lib"`; on macOS you can rewrite the path baked into
the binary with `install_name_tool`). See the
[`goasi`](https://github.com/mikefsq/goasi) README for SDK setup.

## Run

```sh
./asiccd                      # serve on :11111, discovery=direct
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | bind the camera with this serial (hex); recommended |
| `-camera` | `0` | enumeration index (used only when `-serial` is empty) |
| `-discovery` | `direct` | `direct` (self-answer) \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery (direct mode) |

Bind by **serial** in production: it's stable across USB re-enumeration and lets
the service start before the camera is plugged in.
