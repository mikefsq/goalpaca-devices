# asicaa

A standalone ASCOM Alpaca Rotator server for the ZWO CAA (Camera Angle
Adjuster), built on [`goalpaca`](https://github.com/mikefsq/goalpaca) and
[`goasi/caa`](https://github.com/mikefsq/goasi). One process serves one rotator as Alpaca device 0
on its own port.

## Build

```sh
CGO_ENABLED=1 go build -o asicaa ./cmd/asicaa
```

Links `-lCAA` from `/usr/local/lib`. The ZWO SDK is not bundled — download it
from ZWO and install `libCAA` into `/usr/local/lib` (or build with
`CGO_LDFLAGS="-L/path/to/lib"`; on macOS rewrite the binary's path with
`install_name_tool`). See the [`goasi`](https://github.com/mikefsq/goasi) README.

## Run

```sh
./asicaa                      # serve on :11111, discovery=direct
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | bind the rotator with this serial (hex); recommended |
| `-index` | `0` | enumeration index (used only when `-serial` is empty) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
