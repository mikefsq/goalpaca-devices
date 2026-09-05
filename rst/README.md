# rst

A standalone ASCOM Alpaca Telescope server for Rainbow Astro RST harmonic
mounts (RST-135(E) / RST-300), built on [`goalpaca`](https://github.com/mikefsq/goalpaca)
and the [`lx200/rst`](https://github.com/mikefsq/lx200) protocol library. One
process serves one mount as Alpaca device 0 on its own port.

## Build

```sh
go build -o rst ./cmd/rst
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
./rst -serial YOUR_BRIDGE_SERIAL
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `11111` | Alpaca HTTP port |
| `-serial` | "" | USB bridge serial; empty = discover an RST |
| `-discovery` | `direct` | `direct` \| `register` \| `off` |
| `-discovery-server` | `localhost:32227` | proxy address for `register` mode |
| `-ipv6` | false | also answer IPv6 multicast discovery |

## Home and park

`FindHome` moves to the mechanical home at the west horizon. `Park` moves
the tube along the polar axis and stops tracking; `Unpark` resumes tracking.
`SetPark` is unsupported. The `PolarAxis` action moves to the polar axis
without entering the parked state.

`AtHome` and `AtPark` use mechanical axis positions. `HomeFound` reports
whether the mount has homed during this power cycle; gotos require it.
While homing, poll `Slewing`: position reports can remain unchanged until
the move finishes.

## Rates, clock, and limits

`MoveAxis` programs the selected speed before moving, up to 8.33 degrees
per second. `UTCDate` reads the mount's clock and returns an empty string
if the read fails. Setting it preserves the mount's UTC offset and updates
the local date only when needed.

Configure mount mode, motion limits, acceleration, and encoders through the
handset. These settings are not exposed by this driver.

## Alpaca Actions

Device-specific commands the standard ASCOM members don't cover are exposed as
[ASCOM Actions](https://ascom-standards.org/) (`PUT …/action`, `Action`+`Parameters`
form fields). Names are advertised in CamelCase and matched case-insensitively.
Convention: empty `Parameters` reads the current value; a value sets it. A few are
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
B=http://localhost:11111/api/v1/telescope/0/action
curl -s -X PUT $B -d 'Action=GuideRate&Parameters=&ClientID=1'      # read
curl -s -X PUT $B -d 'Action=GuideRate&Parameters=0.8&ClientID=1'   # set
curl -s -X PUT $B -d 'Action=PolarAxis&Parameters=&ClientID=1'      # slew to pole
```

Use `-help` for all flags, or `-schema commented` to generate a JSONC device
file. See the [shared configuration instructions](../README.md#configuration).
