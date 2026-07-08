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

## Home & park (RST-specific)

The RST's mechanical home is the **West horizon** (`FindHome`, `:Ch#`); its park is
the **polar axis** — OTA laid along the RA axis (`Park`). Both stow with tracking
off. A goto is refused until the mount has been homed this power-cycle (a clear
`NotConnected`-style error). `AtHome` is position-based (currently at the captured
home); `AtPark` is a latch (set on Park completion, cleared by `Unpark` or any
slew). See `lx200/docs/rst-command-set.md` for the protocol details.

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
| `ForcePierFlip` | `on` / `off` | force a pre-slew meridian flip (set-only; the mount doesn't report it) |
| `SlewSpeed` | `1`–`3` | read a manual slew-speed preset |
| `SiteName` | `1`–`4` | read a stored site/park name |
| `Voltage` | — | input voltage (V) |
| `MotorLoad` | — | motor load `dec=…,ra=…` (%) |
| `SystemStatus` | — | controller/motor health `tcs=…,dec=…,ra=…` |
| `AutoResume` | — | auto-resume enabled (bool) |
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
