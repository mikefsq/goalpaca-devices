# smpro

ASCOM Alpaca drivers for the StellarMate SM Pro controller board, using
[stellarmate](https://github.com/mikefsq/stellarmate). Run them on the board's
Raspberry Pi. The switch and focuser are separate binaries using separate
hardware subsystems.

## Build and run

From this module on the Pi:

```sh
go build -o smpro-switch ./cmd/smpro-switch
go build -o smpro-focuser ./cmd/smpro-focuser
sudo ./smpro-switch -port 11130
```

Run `sudo ./smpro-focuser -port 11132` in another process if needed. Both
commands default to `11111`, so assign distinct ports when running both.
The service user needs access to the board's device nodes.

Switch options include `-i2cBus` and `-spiDev`. Focuser options include
`-stepperDev`, `-focusMax`, and `-focusSpeed`. Dew-heater PWM selection is
automatic. Use each binary's `-help` for all flags.

## Switch slots

| Id | Name | Range | Writable |
|---|---|---|---|
| 0–3 | Power 1–4 | 0/1 | yes |
| 4–5 | Dew Heater 1–2 | 0–100 %, step 1 | yes |
| 6 | Variable Output (enable) | 0/1 | yes |
| 7 | Variable Voltage | 3–11 V, step 0.1 | yes |
| 8 | Indicator LEDs | 0/1 | yes |
| 9 | User LED | 0/1 | yes |
| 10 | Antenna Power (GP2) | 0/1 | yes |
| 11–12 | Auto Dew 1–2 | 0/1 | yes |
| 13 | Input Voltage | 0–30 V | no (sensor) |
| 14–15 | Power 1 / Dew 1 current | raw ADC 0–4095 | no (sensor) |
| 16 | Variable Output Measured | 0–12 V | no (sensor) |

Variable Voltage reports the setpoint; Variable Output Measured reports
voltage at the connector. The achievable output also depends on the input
supply. Current readings are raw ADC codes; conversion to amps has not been
characterised.

## Auto-dew and weather

Push ambient conditions to the switch through `SetEnvironment`, or configure
an [MGPBox feed](../mgpbox/README.md). Conditions older than five minutes
leave the last heater duty in place.

| Action | Parameters |
|---|---|
| `SetEnvironment` | `{"temperature_c":12.3,"humidity_pct":78.5}`; optional `dewpoint_c` |
| `Conditions` | empty; returns conditions, age, and staleness |
| `SetAutoDew` | `{"channel":1,"on":2,"off":10,"max":100,"enabled":true}` |
| `AutoDew` | `{"channel":1}`; returns settings and current duty |

An Alpaca disconnect leaves hardware running. The focuser reports absolute
step positions without an encoder; configure a suitable travel limit with
`-focusMax`.
