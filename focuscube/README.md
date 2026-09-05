# focuscube

A standalone ASCOM Alpaca Focuser server for Pegasus Astro FocusCube /
FocusCube2 / SMFC / DMFC / ScopsOAG focusers, built on
[`goalpaca`](https://github.com/mikefsq/goalpaca) and the Go
[`pegasus-astro/focuscube`](https://github.com/mikefsq/pegasus-astro) library
(FTDI USB-serial, no vendor SDK). One process serves one focuser as Alpaca
device 0 on its own port.

It's an absolute focuser (`Absolute`, `Position`, `Move`/`MoveTo`, `Halt`),
with temperature read-back where the unit has a probe. The device does not
report a travel limit, so `MaxStep` is host-configured (`-maxstep`).

## Build

```sh
go build -o focuscube ./cmd/focuscube
```

### Linux permissions

The FocusCube is an FTDI USB-serial device (`/dev/ttyUSB*`), which belongs to the
`dialout` group. Add the service user to it:

```sh
sudo usermod -aG dialout "$USER"    # then re-login
```

## Run

```sh
./focuscube -serial FT1ABCDE                 # bind by USB serial (recommended)
./focuscube -index 0 -maxstep 100000       # or by enumeration index
```

Prefer `-serial`: it's stable across USB re-enumeration, disambiguates several FTDI
devices sharing VID `0403`, and lets the service start before the focuser is plugged
in. `-serial` overrides `-index`.

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | bind the FocusCube with this USB serial (stable across replug); overrides `-index` |
| `-index` | `0` | enumeration index (used only when `-serial` is empty) |
| `-maxstep` | `100000` | maximum step (host-side; the device doesn't report a travel limit) |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
