# smpro

ASCOM Alpaca driver for the **StellarMate SM Pro** CM5 astronomy controller
board, over the pure-Go [stellarmate](https://github.com/mikefsq/stellarmate)
library. One board, two Alpaca devices sharing one hardware handle:

| Device | Type | What it exposes |
|---|---|---|
| SM Pro Power | Switch (ISwitchV3) | 4× power outputs, 2× dew heaters (0–100 %), variable output (enable + 3–12 V), indicator/USER LEDs, GP2 antenna power, 2× auto-dew enables, input voltage + 2× current senses (read-only) |
| SM Pro Focuser | Focuser (IFocuserV4) | TMC2209 stepper, open-loop absolute positioning, async Move/IsMoving, Halt |

## Switch slots

| Id | Name | Range | Writable |
|---|---|---|---|
| 0–3 | Power 1–4 | 0/1 | yes |
| 4–5 | Dew Heater 1–2 | 0–100 %, step 1 | yes |
| 6 | Variable Output (enable) | 0/1 | yes |
| 7 | Variable Voltage | 3–12 V, step 0.1 | yes |
| 8 | Indicator LEDs | 0/1 | yes |
| 9 | User LED | 0/1 | yes |
| 10 | Antenna Power (GP2) | 0/1 | yes |
| 11–12 | Auto Dew 1–2 | 0/1 | yes |
| 13 | Input Voltage | 0–30 V | no (sensor) |
| 14–15 | Power 1 / Dew 1 current | raw ADC 0–4095 | no (sensor) |

The variable output's achievable ceiling tracks the input supply (≈0.86 × Vin),
so a commanded 12.0 V reads back as what the DAC can actually deliver. The
current senses are raw 12-bit codes: their amps scaling has not been
characterised, and an honest raw beats a made-up unit.

## Auto-dew and the weather feed

The board has no weather sensor; an ObservingConditions client pushes ambient
conditions through the Switch device's Action seam, and the stellarmate library
ramps each auto-dew-enabled heater from the temperature/dew-point margin.
Conditions older than five minutes stop steering (the last duty holds; the
`Conditions` action reports the staleness).

```
Action=SetConditions  Parameters={"tempC":12.3,"humidity":78.5}     # dewPointC optional
Action=Conditions     Parameters=                                    # → weather + age + stale
Action=SetAutoDew     Parameters={"channel":1,"on":2,"off":10,"max":100,"enabled":true}
Action=AutoDew        Parameters={"channel":1}                       # → ramp + current duty
```

## Run

```sh
make smpro && sudo ./bin/smpro          # Alpaca on :11130, discovery on
sudo ./bin/smpro -port 11130 -discovery off
```

Flags `-i2c`, `-spi`, `-pwmchip`, `-tty` override the board wiring; the
stellarmate library auto-detects CM4 vs CM5 for the dew-heater pwmchip.

`sudo` (or udev rules for i2c/spi/gpio/tty) is required for the device nodes.
