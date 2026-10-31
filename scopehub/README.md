# scopehub

ASCOM Alpaca Switch driver for the [scope_hub](https://github.com/mikefsq/scope_hub) board. 

```sh
go build -o scopehub ./cmd/scopehub
./scopehub -serial 8E70418DA7737E15 -port 11140
```

`-serial` is the RP2350 unique ID from `scopehub list`; without it the first board is used.

## Switches

| Id | Name | Range | Writable |
|---|---|---|---|
| 0 | Input Voltage | V | no |
| 1 | Input Current | A | no |
| 2 to 7 | DC 1 to DC 6 | 0 off, 1 on | yes |
| 8 to 13 | USB 1 to USB 6 | 0 off, 1 on | yes |
| 14 to 19 | DC n Duty | 0 to 100% | yes |
| 20 to 25 | DC n Dew Auto | 0 manual, 1 auto | yes |
| 26 | LED Brightness | 0 to 100% | yes |
| 27 to 32 | DC n Current | A | no |
| 33 to 38 | USB n Current | A | no |
| 39 to 44 | USB n Attached | 0 or 1 | no |
| 45 | Temperature Offset | -20 to 20 °C | yes |
| 46 | Dew Start Margin | 0 to 30 °C | yes |
| 47 | Dew Full Margin | -10 to 30 °C | yes |
| 48 | Dew Min Duty | 0 to 100% | yes |
| 49 | Dew Max Duty | 0 to 100% | yes |
| 50 | DC PWM Frequency | 0.1 to 10 Hz | yes |
| 51 | LED Auto | 0 manual, 1 auto | yes |
| 52 | Air Temperature | °C | no |
| 53 | Dew Point | °C | no |
| 54 | Dew Margin | °C | no |
| 55 | Light Level | lux | no |
| 56 to 61 | DC n PWM | 0 steady 100%, 1 run at the duty cycle | yes |

## Actions

| Action | Effect |
|---|---|
| `SaveSettings` | write the live settings to flash |
| `RestoreDefaults` | restore default settings in RAM |
| `Status` | every reading as JSON |
| `SwitchLayout` | page layout as JSON, see `layout.go` |

## License

[AGPL-3.0-only](../LICENSE).
