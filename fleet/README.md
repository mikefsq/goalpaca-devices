# astrofleet

One ASCOM Alpaca server that hosts several `goalpaca_devices` drivers in a single
process, on a single port, with a single discovery responder — instead of running
one binary (and one systemd unit) per device.

It is a thin **composition** layer: each device driver still owns its own hardware
logic (acquire → monitor → re-acquire). The fleet just registers the devices you
enable in a config file and runs the shared server.

## Configure

Which devices run is declared in a JSON config (`-config`, default `fleet.json`).
A device runs if it's in the list and not disabled. To turn one off without
deleting its entry (and its serial/port/options), set `"enable": false` — omitted
means enabled. See [`config/fleet.example.json`](config/fleet.example.json).

```json
{ "driver": "focuslynx", "index": 0, "channel": 1, "enable": false }
```

```json
{
  "port": 11200,
  "discovery": "direct",
  "devices": [
    { "driver": "tenmicron", "addr": "10.0.1.51:3492", "aperture": 200, "focalLength": 1600 },
    { "driver": "asicam", "serial": "1a2b3c4d", "name": "Main camera" },
    { "driver": "oasisfoc", "index": 0 },
    { "driver": "oasisfw", "index": 0 }
  ]
}
```

Each entry becomes one Alpaca device. Device **numbers are assigned per ASCOM type**
in list order (the first `focuser` is `focuser/0`, the next `focuser/1`, …).

### Drivers and their binding fields

| `driver` | ASCOM type | bind by | other fields |
|---|---|---|---|
| `tenmicron` | telescope | `addr` (TCP) | `aperture`/`focalLength` (**mm**), `apertureArea` (m²), `guiderAperture`/`guiderFocalLength` (mm) |
| `asiam5` | telescope | `serial` or `addr` | |
| `onstep` | telescope | `serial` or `addr` | |
| `rst` | telescope | `serial` (auto-detect if empty) | |
| `asicam` | camera | `serial` (preferred) or `index` | |
| `asieaf` | focuser | `index` | |
| `oasisfoc` | focuser | `index` | |
| `focuscube` | focuser | `index` | `maxstep` (default 100000) |
| `focuslynx` | focuser | `index` | `channel` (1 or 2) |
| `asiefw` | filterwheel | `serial` or `index` | `unidirectional` |
| `oasisfw` | filterwheel | `index` | |

All entries accept an optional `name` to override the device's display name.

Prefer `serial`/`addr` where the driver supports it: it is a stable identity that
survives unplug/replug and multi-device index shuffles. Index binding selects the
Nth attached unit of that kind and is fine for a single device.

### Extra front-ends: INDI and LX200

Beyond Alpaca, the same device objects can be served over other native protocols —
no `indiserver`, no translation layers, just sibling front-ends onto the one mount
object (state stays consistent across all of them).

- **INDI** (PHD2): set a top-level `"indi": { "enable": true, "port": 7624 }`.
  One in-process INDI server multiplexes devices by **device name** on a single port —
  so point PHD2 at `host:7624` and pick the device by the `name` you gave it. INDI has
  no discovery, so the port is fixed and names must be unique. Membership is **opt-in**:
  a device joins only when it sets `"indi": true` (default Alpaca-only). Mounts join as
  a telescope+guider; **`asicam` cameras join as a CCD guide camera** (CCD_INFO pixel
  size + FITS frames, plus the camera's gain, offset, and subframe ROI as `CCD_CONTROLS`
  and `CCD_FRAME`, defined on connect with the camera's live ranges) — so set
  `"indi": true` on your guide camera to drive it from PHD2.
  A mount's `"guideRate"` (fraction of sidereal, default 0.5) is reported to PHD2;
  tenmicron and am5 report their *actual* rate from the mount and ignore this, while
  rst/onstep use the configured value.
- **LX200** (Stellarium, SkySafari): set a top-level `"lx200": { "enable": true, "basePort": 4030 }`.
  Because LX200 can't multiplex (one connection = one mount), **every mount** gets its
  own server, assigned a port from `basePort` upward (4030, 4031, …). A mount can pin a
  specific port with `"lx200Port": N` (which also enables LX200 for just that mount even
  if the fleet block is off). Connect Stellarium's TelescopeControl as "Meade LX200".

```json
{
  "discovery": "direct",
  "indi":  { "enable": true, "port": 7624 },
  "lx200": { "enable": true, "basePort": 4030 },
  "devices": [
    { "driver": "tenmicron", "name": "10Micron", "addr": "10.0.1.51:3492", "indi": true },
    { "driver": "asicam", "serial": "5e6f7a8b", "name": "Guide camera" }
  ]
}
```

Alpaca clients (NINA) still auto-discover via UDP 32227 as before; these are additive.

The discovery responder answers both IPv4 broadcast and IPv6 multicast (group
`ff12::a1:9aca`), so clients find the fleet over whichever they use. IPv6 is on by
default and joins the group on every multicast-capable interface; set `"ipv6": false`
to bind IPv4 only. On a host with no usable IPv6 the responder logs once and IPv4 is
unaffected.

#### Restricting which interfaces the fleet serves (`listen`)

By default every server binds the wildcard (`:port`) — all interfaces, both IP
stacks. Set top-level `"listen"` to restrict the fleet to specific interfaces. It is
applied **consistently** to the Alpaca device servers, the LX200 bridges, the INDI
hub, *and* discovery (which then only advertises where the fleet actually listens).
Each entry is either:

- **an interface name** (e.g. `"en0"`, or `"eth0"`/`"lo"` on Linux) — expands to *all*
  of that interface's addresses: IPv4, IPv6 global, and IPv6 link-local. **Use this to
  serve both IP stacks** on an interface. Robust to DHCP since the name is stable.
- **an IP literal** (e.g. `"10.0.1.20"`) — binds exactly that one address. Note a bare
  IPv4 literal serves **IPv4 only** (no IPv6); name the interface to get both.

```json
"listen": ["lo", "eth0"]
```

serves loopback and the wired NIC (both stacks) and keeps the fleet off everything
else — e.g. a VM bridge. The startup log prints one line per bound address. Omit
`listen` to bind all interfaces.

### Simulated devices (client development)

For developing a client with no hardware, the fleet has `sim-*` drivers — one per
ASCOM type: `sim-telescope`, `sim-camera`, `sim-focuser`, `sim-filterwheel`,
`sim-rotator`, `sim-switch`, `sim-dome`, `sim-covercalibrator`,
`sim-observingconditions`, `sim-safetymonitor`. A ready-made config is included:

```sh
go run . -config config/fleet.sim.json    # a full simulated fleet, each on its own port + discovery
```

`sim-telescope` and `sim-camera` also light up the **INDI front-ends** — the mount
appears over INDI (`:7624`) and LX200 (`:4030`), and a `sim-camera` with `"indi": true`
appears as an **INDI guide camera** (CCD_INFO pixel size + FITS frames over the CCD1
BLOB). The sim camera exposes no gain/offset/subframe, so — unlike `asicam` — it omits
`CCD_CONTROLS`/`CCD_FRAME`. So `fleet.sim.json` is a full no-hardware testbed: PHD2 can connect the sim mount
*and* the sim guide camera over INDI and run the whole guide loop, Stellarium can slew
the mount over LX200, and your Alpaca client drives everything. The remaining `sim-*`
devices are Alpaca-only until the matching INDI device types exist.

The simplest no-config path is `make sim` from `goalpaca-devices`, which runs every
simulated device behind one Alpaca port (the standalone `alpacasim` binary).

## Plug / unplug

Every enabled device is registered at startup and gets its own
acquire/monitor/re-acquire goroutine, **whether or not its hardware is attached**.
So you can start the fleet on an empty bus and devices are picked up within a few
seconds of being plugged in; an unplug only marks that one device disconnected and
does not affect the others.

The set of devices is fixed for the process lifetime (ASCOM's configured-device
list is static per server). Adding a **new** device that isn't in the config means
editing the config and restarting — cheap, since ASCOM clients re-read the device
list when they (re)connect.

## Logging

Everything logs to stderr, so under systemd it all lands in the journal
(`journalctl -u astrofleet -f`):

- **Client requests / responses** — verbose per-request Alpaca HTTP logging and
  per-message INDI traffic. **Off by default**; set `"debug": true` in the config to
  enable. Lifecycle logs below (listening lines, device and INDI client
  connect/disconnect) print regardless of `debug`.
  ```
  alpaca 2026/06/08 21:14:03 10.0.1.20:51514 GET /api/v1/telescope/0/rightascension -> 200 (1ms)
  indi: <- 10.0.1.20:51520 getProperties device="" name=""
  ```
- **Device connect / disconnect** — each driver logs when it acquires its hardware
  and when it loses it (with the error that caused the drop, then it reconnects):
  ```
  tenmicron: mount 10micron-10.0.1.51:3492 connected
  oasisfoc: focuser Oasis-fw0 lost (read timeout); re-acquiring
  ```
- **Errors** — a device that can't be reached logs the failure once (not on every
  retry), and request failures show up as non-2xx statuses in the request log.
  ```
  tenmicron: mount 10micron-10.0.1.51:3492 connect failed: dial tcp 10.0.1.51:3492: connect: no route to host (retrying)
  ```

## Build & run

```sh
go build -o astrofleet .
./astrofleet -config fleet.json
```

Cross-compile for a Raspberry Pi CM5 (no C toolchain needed):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o astrofleet .
```

USB/HID drivers on Linux need usbfs/hidraw access. The Go ASI camera driver
talks to the camera over usbfs (`/dev/bus/usb/*`) and binds by **factory serial** —
which ASI cameras expose *not* as a USB descriptor (so `lsusb`/`dmesg` never show it)
but in flash, read via a vendor control transfer once the device is opened. So serial
binding needs read-write access to the device node. Install the shipped rules:

```sh
sudo cp deploy/99-astrofleet.rules /etc/udev/rules.d/
sudo udevadm control --reload && sudo udevadm trigger   # then replug the camera
```

`deploy/install.sh` does this for you. The file covers ZWO (`03c3`) and PlayerOne
(`a0a0`); add other vendors' `idVendor` lines as you wire their drivers in.

## Deploy as a systemd service

Layout:

- `config/fleet.example.json` — starter config (copy to `/etc/astrofleet/fleet.json`)
- `deploy/astrofleet.service` — systemd unit
- `deploy/install.sh` — installs the binary, config, and unit, then enables the service

On the target (run from the `fleet/` directory, as root):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o astrofleet .   # or copy a prebuilt binary
sudo deploy/install.sh ./astrofleet
sudo nano /etc/astrofleet/fleet.json    # edit for your hardware
sudo systemctl restart astrofleet
journalctl -u astrofleet -f             # watch logs
```

The unit installs the binary at `/usr/local/bin/astrofleet` and reads
`/etc/astrofleet/fleet.json`. It restarts on failure (`Restart=on-failure`,
`RestartSec=5`) — the safety net for the shared-process model, since one device's
panic currently takes the whole fleet down. It runs as `root` by default for
fuss-free USB/serial/hidraw access; the unit file comments show how to switch to a
dedicated user with the `dialout`/`plugdev` groups to harden it.
