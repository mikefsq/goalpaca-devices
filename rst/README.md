# rst

A standalone ASCOM **Alpaca Telescope** server for Rainbow Astro RST harmonic
mounts (RST-135(E) / RST-300), built on [`goalpaca`](https://github.com/mikefsq/goalpaca)
and the [`lx200/rst`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build .          # Go, no SDK
```

### Linux permissions

The RST's FTDI USB-serial adapter (`/dev/ttyUSB*`) is in the `dialout` group. Add the
service user to it:

```sh
sudo usermod -aG dialout "$USER"    # then re-login
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

## Home and park

Two mechanical positions, mapped straight onto ASCOM. There is no arbitrary park — the RST-135
community manual confirms both are fixed, and lists a Dec=90 park as a *wanted* feature, which
is what `Park` (the OTA along the RA axis, pointing at the pole) provides.

| ASCOM | RST |
|---|---|
| `FindHome` / `AtHome` / `CanFindHome` | the **West horizon** mechanical home (`:Ch#`) |
| `Park` / `AtPark` / `CanPark` / `CanUnpark` | the **polar axis** — OTA laid along the RA axis |
| `SetPark` / `CanSetPark` | not supported: neither position is arbitrary |

Parking stops tracking; `Unpark` re-enables it. The `PolarAxis` action slews to the park
position *without* entering the parked state, for polar alignment.

Three home notions are easy to confuse, so the driver keeps them distinct:

- **`AtHome`** — the ASCOM property, a latch set only when a `FindHome` completes and reset by
  any slew. A mount merely sitting at the home coordinates reports false.
- **`HomeFound`** — set once the mount has homed this power-cycle, seeded from the mount's `:GH#`
  latch on connect. This is what gates gotos.
- the mount's **`:AH#`** — reports whether a home seek is *executing*, and reads false right
  after one succeeds. Not exposed as an ASCOM member; it is the mount's homing busy guard.

`AtPark` prefers its completion latch and falls back to comparing position against the pole, so
it still reports parked after reconnecting to a mount someone else parked.

Homing runs at a fixed rate the slew presets do not affect, and the mount **stops reporting
position while it seeks** — coordinates read as the starting position until it finishes (38 s
from the pole, on hardware). Poll `Slewing`, not coordinates. A goto is refused until the mount
has homed this power-cycle.

The handset's ten `Parking N` presets are not reachable over the serial port.

See [`lx200/rst/PROTOCOL.md`](../../lx200/rst/PROTOCOL.md) for the full protocol.

## Slew rates

`:RG#`/`:RC#`/`:RM#`/`:RS#` do not carry a rate — each selects one of four speed *slots*
(`:CU0#`–`:CU3#`) whose values are stored on the mount and written by anything, including
the handset. Selecting without setting therefore delivers an unknown speed.

`MoveAxis` programs the slot first, so the rate a client asks for is the rate it gets. The
advertised ceiling is the vendor's fastest, **8.33 °/s** (2000× sidereal); one degree per
second is 240× sidereal.

## Clock

`UTCDate` returns the **mount's** clock (reconstructed from `:GL#`/`:GC#`/`:GG#`), and
`SetUTCDate` sets it over serial. The vendor's own ASCOM driver never implemented the setter —
the community manual lists it as absent and recommends leaving the handset in to set time from
GPS — so this is a capability past the reference driver, and it matters: the mount computes
sidereal time from this clock, and a GPS fix does *not* re-sync it once it has drifted. Syncing
on connect is cheap (`SetUTC` writes only `:SL#`, leaving the offset and date alone).

## Meridian and limits

The mount enforces upper/lower declination limits and a meridian limit, set in the handset in
integer degrees. A goto past a limit is refused (surfaced as `InvalidOperation`) or triggers a
flip. These are not settable over serial. Per the community manual, do not set the meridian
limit to exactly zero — clock and location differences between the driver and the mount can
cause unexpected flips, which the clock drift on this mount makes a real risk.

## Not settable over Alpaca (or serial)

The mount's own setup lives in the handset, not the serial protocol. Mount mode
(Equatorial / AltAz), drift correction, acceleration, and the encoder configuration
cannot be changed by this driver — no serial command reaches them. Auto-resume
(`AutoResume` action) is the one configuration item that is. See
[`lx200/rst/PROTOCOL.md`](../../lx200/rst/PROTOCOL.md) for the full accounting.

## Alpaca Actions

Device-specific commands the standard ASCOM members don't cover are exposed as
[ASCOM Actions](https://ascom-standards.org/) (`PUT …/action`, `Action`+`Parameters`
form fields). Names are advertised in CamelCase and matched **case-insensitively**.
Convention: **empty `Parameters` reads the current value; a value sets it.** A few are
read-only, take an index, or are operations.

| Action | Parameters | Behavior |
|---|---|---|
| `PolarAxis` | — | slew the OTA to the polar axis (async — poll `slewing`) |
| `GuideRate` | *(empty)* read · `<x>` set | guide rate in ×sidereal (also the standard `GuideRateRightAscension`/`Declination`, in °/s) |
| `ForcePierFlip` | *(empty)* read · `on` / `off` set | force a pre-slew meridian flip; the mount reports it via `:AF#` |
| `SlewSpeed` | `1`–`3` | read a manual slew-speed preset |
| `SiteName` | `1`–`3` | read a stored site name (`:GP#` is the precision mode, not a fourth name) |
| `Voltage` | — | input voltage (V) |
| `MotorLoad` | — | motor load `dec=…,ra=…` (%) |
| `SystemStatus` | — | controller/motor health `tcs=…,dec=…,ra=…` |
| `AutoResume` | *(empty)* read · `on`/`off` set | auto-resume enabled (bool) |
| `LocalTime` | — | mount clock (hours) |
| `Date` | — | mount date (MM/DD/YY) |
| `UTCOffset` | — | hours to add to local for UTC |
| `HomeFound` | — | has the mount been homed this power-cycle (bool) |
| `Fault` | — | last motion-abort token, or `none` |
| `SetOptics` | JSON | set the instrument profile (aperture/focal length, mm) |

Example:

```sh
B=http://localhost:11202/api/v1/telescope/0/action
curl -s -X PUT $B -d 'Action=GuideRate&Parameters=&ClientID=1'      # read
curl -s -X PUT $B -d 'Action=GuideRate&Parameters=0.8&ClientID=1'   # set
curl -s -X PUT $B -d 'Action=PolarAxis&Parameters=&ClientID=1'      # slew to pole
```
