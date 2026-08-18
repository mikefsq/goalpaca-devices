# goalpaca-devices

Standalone **ASCOM Alpaca** device drivers built on the 
[goalpaca](https://github.com/mikefsq/goalpaca) server library. These drivers 
are intended to be distributed as source code without vendor library dependencies and 
are intended to be run on a raspberry pi or similar hardware. 

Each driver connects to a hardware device and serves an Alpaca server. Each will 
respond using the standard Alpaca shared discovery port 32227 so can be used by 
any client on the local network that supports Alpaca devices. Each driver is 
responsible for handling the device connects, disconnects, reconnects, and to 
an extent fault recovery, so the Alpaca client will receive a stable interface
to the devices it needs. 

A simple setup (beyond just running the cmd directly) uses a systemd script to
launch needed drivers.  If there are many drivers it is convenient to
use the composed server [alpacahurd](https://github.com/mikefsq/alpacahurd).

Most of these drivers are **Go with no vendor SDK** and run on Linux and macOS
hosts. The device protocols were implemented directly. 

## Telescopes

LX200-family mounts, built on [`lx200`](https://github.com/mikefsq/lx200). 

| Dir | Mount | Connect | Port |
|---|---|---|---|
| `tenmicron` | 10Micron GM-series | TCP | 11200 |
| `rst` | Rainbow Astro RST-135/300 | USB-serial | 11202 |
| `onstep` | OnStep / OnStepX | USB-serial or WiFi/TCP | 11203 |
| `asiam5` | ZWO AM3/AM5/AM5N/AM7 | USB-serial or WiFi/TCP | 11201 |

## Cameras

| Dir | Camera | Backend |
|---|---|---|
| `astrocam` | ASI6200, ASI174MM, ASI290MM, etc. | **Go** [`astrocam`](https://github.com/mikefsq/astrocam) — no SDKs |
| `ptpcam` | USB PTP cameras (Fuji, Sony) | **Go** [`ptp`](https://github.com/mikefsq/ptp) — no SDKs |

## Focusers

| Dir | Focuser | Backend |
|---|---|---|
| `oasisfoc` | Astroasis Oasis | **Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) |
| `focuscube` | Pegasus FocusCube / DMFC | **Go**, USB-serial ([`pegasus-astro`](https://github.com/mikefsq/pegasus-astro)) |
| `focuslynx` | Optec FocusLynx / ThirdLynx | **Go**, USB-serial ([`optec`](https://github.com/mikefsq/optec)) |
| `asieaf` | ZWO EAF | **Go**, USB-HID ([`goasi/eaf`](https://github.com/mikefsq/goasi)) |

## Filter wheels

| Dir | Wheel | Backend |
|---|---|---|
| `oasisfw` | Astroasis Oasis | **Go**, USB-HID ([`oasis-astro`](https://github.com/mikefsq/oasis-astro)) |
| `asiefw` | ZWO EFW | **Go**, USB-HID ([`goasi/efw`](https://github.com/mikefsq/goasi))|

## Rotators

| Dir | Rotator | Backend |
|---|---|---|

## Observing conditions

Weather / sky-quality sensors, exposed as ASCOM **ObservingConditions**. 

| Dir | Device | Backend |
|---|---|---|
| `unihedron` | Unihedron SQM (sky quality meter) | **Go**, USB-serial ([`unihedron`](https://github.com/mikefsq/unihedron)) |
| `mgpbox` | Astromi.ch MGPBox (GPS + weather) | **Go**, USB-serial ([`astromi.ch`](https://github.com/mikefsq/astromi.ch)) |

Note: This MGPBox driver can feed its GPS + weather data into the goalpaca `tenmicron` driver (site coordinates,
clock, and refraction pressure/temperature) via the `mountAddr` config field.

## Simulator

| Dir | Device | Backend |
|---|---|---|
| `sim` | Simulated mount + guide camera on **one shared sky model** | **Go**, no hardware |

Note: This simulated device connects the mount coordinates with a simulated star field 
for the guide camera. So it presents one binary with both devices (telescope 0 + camera 0) 
on one port. A guiding client like PHD2 can calibrate and guide a closed loop with no hardware. 
The camera scale is configurable, see driver documentation. 


## The herd

Rather than launch each driver by hand,
**[alpacahurd](https://github.com/mikefsq/alpacahurd)** (its own repo) runs the
enabled devices from one configuration: `hurd.json` for the shared blocks and a
`devices.d` directory with one file per device. Drivers can be compiled into
the alpacahurd binary or run as separate binaries under the platform supervisor;
both layouts are supported and a deployment may mix them. Each device serves on
its own Alpaca port, or several share one, and one UDP-32227 discovery responder
answers for all of them. Every device has its own acquire and reconnect loop, so
it survives an empty bus and unplug or replug.

alpacahurd also serves two optional native front-ends over the same device
objects: an LX200 endpoint for Stellarium or SkySafari, and limited INDI mount
and camera interfaces intended for PHD2. PHD2 now supports Alpaca directly, so
the INDI guide path is no longer needed.

You may find dpkgs for raspbian (and also windows and osx builds) containing Alpaca here: [PHD2](https://github.com/mikefsq/phd2).

## Setup forms

Each driver's browser configuration page is generated from its tagged config
struct; the driver writes no form code. [SETUP_FORMS.md](SETUP_FORMS.md) covers
the tags, the start-time versus live distinction, and the `Reconfigure` hook.


## Build

From this directory the `Makefile` builds into `./bin`:

```sh
make                # every Go driver
make tenmicron      # one driver
make sim            # the coupled guide sim (mount + camera, one shared sky)
make alpacasim      # run goalpaca's one-of-every-type protocol sim (not guidable)
```

Or build any module in place: `cd tenmicron && go build .`, then e.g.
`./tenmicron -addr 10.0.1.51:3492`. The Go drivers (telescopes, `astrocam`,
`oasisfoc`, `oasisfw`, `focuslynx`, `focuscube`, `unihedron`, `mgpbox`). 

See each driver's `README.md` for details. 

## Dependencies

These drivers depend on the goalpaca server library and underlying device driver libraries:

- [`goalpaca`](https://github.com/mikefsq/goalpaca) — the Alpaca server framework (all drivers)
- Go device libraries: [`astrocam`](https://github.com/mikefsq/astrocam) (CMOS camera like the ASI6200, ASI174MM, etc.),
  [`lx200`](https://github.com/mikefsq/lx200) — mount protocol libraries (10 Micron, Rainbow Astro, OnStep).
  [`oasis-astro`](https://github.com/mikefsq/oasis-astro) (Focuser, Filter Wheel), 
  [`optec`](https://github.com/mikefsq/optec) (Optec Focuser),
  [`pegasus-astro`](https://github.com/mikefsq/pegasus-astro) (Pegasus Focuser),
  [`unihedron`](https://github.com/mikefsq/unihedron) (SQM sky-quality meter),
  [`astromi.ch`](https://github.com/mikefsq/astromi.ch) (MGPBox GPS/weather)

## License

MIT — see [LICENSE](LICENSE).
