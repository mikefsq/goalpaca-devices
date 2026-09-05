# asiair

ASCOM Alpaca Switch driver for the ZWO ASIAIR power board, using
[goasi/asiair](https://github.com/mikefsq/goasi). It runs on the ASIAIR's
own Raspberry Pi, with direct access to GPIO and I²C.

## Build and run

On the ASIAIR Pi, from this module:

```sh
go build -o asiair ./cmd/asiair
sudo ./asiair -port 11131
```

The default HTTP port is `11111`. The registered driver name is
`asiair-switch`. Use `-help` for options.

## Switch array (17 slots)

| Id | Name | Write | Range |
|---|---|---|---|
| 0–1 | Port 1, Port 2 | yes | 0–100 (duty %) — dimmable |
| 2–3 | Port 3, Port 4 | yes | 0–1 (on/off) |
| 4 | DSLR Shutter | yes | 0–1 (1 = contact closed, exposing) |
| 5–6 | Auto Dew (Port *n*) | yes | 0–1 |
| 7–8 | Input Voltage, Input Current | no | read-only |
| 9–12 | Port 1–4 Voltage | no | read-only |
| 13–16 | Port 1–4 Current | no | read-only |

Ports 1 and 2 support dimming when the required `pwm-2chan` overlay is
loaded. Without it they accept only 0 or 100 percent. The standalone
command uses the default board wiring.

The DSLR shutter remains closed while its value is 1. Use the sequence
Actions for timed exposures.

## Actions

| Action | Params | Does |
|---|---|---|
| `SetEnvironment` | `{"temperature_c":8,"humidity_pct":82}` | push conditions; empty params reads them back |
| `SetAutoDew` | `{"port":1,"on":2,"off":10,"max":100,"enabled":true}` | the auto-dew ramp |
| `AutoDew` | `{"port":1}` | read the ramp + the current duty |
| `StartSequence` | `{"frames":20,"exposure_s":120,"delay_s":3}` | run a DSLR sequence |
| `AbortSequence` | — | stop it; the shutter opens |
| `SequenceStatus` | — | progress |

`SetEnvironment` also accepts an optional `dewpoint_c`. A missing or zero
dew point is derived from temperature and humidity. The payload can include
other weather fields used by the MGPBox feed; unused fields are ignored.
`SetAutoDew` accepts `channel` as an alias for `port`.

## Power and hardware setup

An Alpaca disconnect leaves the board open and the ports powered. Stopping
the server releases GPIO requests and can cut power to the on/off ports.
Keep the server running while attached equipment needs power.

The pin map and ADC scaling were derived from ZWO's driver and still need
hardware verification. Check the goasi ASIAIR documentation before operating
the board, including its read-only ADC scan.
