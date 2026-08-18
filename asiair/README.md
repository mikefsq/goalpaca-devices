# asiair

ASCOM Alpaca driver for the **ZWO ASIAIR** power-distribution board, over the
pure-Go [`asiair`](https://github.com/mikefsq/asiair) library. One **Switch**
device; no vendor SDK; no cgo.

```sh
go build ./cmd/asiair
sudo ./asiair -port 11131
```

The ASIAIR is a Raspberry Pi carrier with no microcontroller in between — the Pi
drives the power FETs through its own GPIOs and reads the telemetry ADCs itself.
So this driver runs **on the ASIAIR's own Pi**, in place of ZWO's firmware, not
alongside it.

## Switch array (17 slots)

| Id | Name | Write | Range |
|---|---|---|---|
| 0–1 | Port 1, Port 2 | yes | 0–100 (duty %) — **dimmable** |
| 2–3 | Port 3, Port 4 | yes | 0–1 (on/off) |
| 4 | DSLR Shutter | yes | 0–1 (1 = contact closed, exposing) |
| 5–6 | Auto Dew (Port *n*) | yes | 0–1 |
| 7–8 | Input Voltage, Input Current | no | read-only |
| 9–12 | Port 1–4 Voltage | no | read-only |
| 13–16 | Port 1–4 Current | no | read-only |

**Only ports 1 and 2 can dim.** That is the Pi's silicon, not a driver limit: the
PWM peripheral reaches the header on two channels only. Put the dew heater or flat
panel there. With `-ports24` the pairing moves to ports 2 and 4 (and needs the
matching overlay); the slot count stays at 17 either way, so a saved client profile
survives the change, and the auto-dew slots are named for the ports they actually
control.

If the `pwm-2chan` overlay is missing, the library falls back to driving those
ports on/off and logs a warning. The slots still accept 0 and 100 — the port really
can be switched — but a fractional duty then fails with an error naming the missing
overlay, rather than silently rounding up to full power and leaving you to wonder
for a season why the heater only ever runs flat out.

The **DSLR shutter is a level, not a momentary**. The ASCOM Switch FAQ advises
momentary action on one edge of `SetSwitch`, but a bulb exposure requires the
contact to be *held* closed — a momentary switch could not express what the line
does. Timed runs go on the Action seam.

## Actions

| Action | Params | Does |
|---|---|---|
| `SetEnvironment` | `{"temperature_c":8,"humidity_pct":82[,"dewpoint_c":6]}` | push conditions; empty params reads them back |
| `SetAutoDew` | `{"port":1,"on":2,"off":10,"max":100,"enabled":true}` | the auto-dew ramp |
| `AutoDew` | `{"port":1}` | read the ramp + the current duty |
| `StartSequence` | `{"frames":20,"exposure_s":120,"delay_s":3}` | run a DSLR sequence |
| `AbortSequence` | — | stop it; the shutter opens |
| `SequenceStatus` | — | progress |

`SetEnvironment` takes the **same payload as `smpro` and `tenmicron`**, field for
field, so one weather producer (an MGPBox, say) broadcasts a single snapshot to
every device that wants it. The mount takes pressure and temperature for
refraction; a power board takes temperature, humidity and dew point for auto-dew;
each ignores the rest. A `dewpoint_c` of literal 0 is *not* trusted — feeders that
don't compute one still send the field, and taking it at face value would read as a
margin of the whole air temperature ("bone dry") and switch the heaters off on
exactly the night they're needed. It is derived from temperature and humidity
instead.

`SetAutoDew` names the port `"port"`; `"channel"` is accepted as an alias, so an
smpro-shaped client works unchanged on the default pairing.

## Disconnect does not cut power

The board's on/off ports are held by GPIO character-device line requests, and the
kernel reverts a line the moment its request fd closes — the board's pull-down then
switches the port **off**. Ports 3 and 4 are where the camera and the mount live.

So this driver separates two things that are easy to conflate:

- The **board** is hardware. Opened once at startup, released once at teardown.
  Owned by the *process*.
- **Connected** is an ASCOM client's logical session. `Disconnect` ends it —
  `Connected` goes false and operational members fault with `NotConnected`, as
  ASCOM requires — but the board stays open and **the ports stay powered**.

Get that wrong and a client reconnecting mid-session power-cycles the mount, losing
its position, and the camera, losing its cooling. The asymmetry is what makes it
nasty rather than merely wrong: the *dimmable* ports run through sysfs PWM, which
survives a process exit. A driver that dropped its lines would cut the mount and
leave the dew heater running.

Stopping the server *is* a real power event for ports 3 and 4. That is correct — a
driver that has stopped running should not leave rails it can no longer control —
but it means the server is the owner of the power state, and it has to keep running.

## Not yet verified

The board's pin map, channel map and I²C bus number are decoded from ZWO's INDI
driver, not from a datasheet. Run the library's read-only `adsscan` first — it finds
the ADCs by response, cannot switch a port on, and settles the config value most
likely to be wrong. The main-current scaling in particular does not survive
arithmetic as a bare shunt; see the library's README.

## Conformance

`go test ./...` runs the ISwitchV3 invariants over a real HTTP server and client
with the board faked: metadata linkage, step consistency, invalid-id rejection,
range validation, and the NotConnected gating — under **both** PWM pairings, since
the slot metadata is built per device from the pairing rather than being a fixed
table.

It does not run `conformance.CheckSwitch` wholesale. That harness is written against
goalpaca's sim switch and assumes every slot is writable, that `SetSwitchName`
round-trips, and that slot 0 is analog. None of those hold for a device with
read-only sensors, and ISwitchV3 does not require them — `CanWrite` exists precisely
so a switch can be a sensor. (`smpro` would fail the same three checks.)
