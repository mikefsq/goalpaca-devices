scopehub serves ASCOM Alpaca Switch and ObservingConditions for the [scope_hub](https://github.com/mikefsq/scope_hub) board.

```sh
go build -o scopehub ./cmd/scopehub
./scopehub -serial 8E70418DA7737E15 -port 11140
```

`-serial` takes RP2350 unique ID from `scopehub list`. No serial picks first board.

Switch channels below give id, name, range, writable.

- 0 Input Voltage, V, no.
- 1 Input Current, A, no.
- 2 to 7 DC 1 to DC 6, 0 off, 1 on, yes.
- 8 to 13 USB 1 to USB 6, 0 off, 1 on, yes.
- 14 to 19 DC n Duty, 0 to 100%, yes.
- 20 to 25 DC n Dew Auto, 0 manual, 1 auto, yes.
- 26 LED Brightness, 0 to 100%, yes.
- 27 to 32 DC n Current, A, no.
- 33 to 38 USB n Current, A, no.
- 39 to 44 USB n Attached, 0 or 1, no.
- 45 Temperature Offset, -20 to 20 °C, yes.
- 46 Dew Start Margin, 0 to 30 °C, yes.
- 47 Dew Full Margin, -10 to 30 °C, yes.
- 48 Dew Min Duty, 0 to 100%, yes.
- 49 Dew Max Duty, 0 to 100%, yes.
- 50 DC PWM Frequency, 0.1 to 10 Hz, yes.
- 51 LED Auto, 0 manual, 1 auto, yes.
- 52 Air Temperature, °C, no.
- 53 Dew Point, °C, no.
- 54 Dew Margin, °C, no.
- 55 Light Level, lux, no.
- 56 to 61 DC n PWM, 0 steady 100%, 1 run at duty cycle, yes.
- 62 Pressure, hPa station pressure, no.
- 63 Barometer Temperature, °C raw chip temperature, no.

Barometer readings follow firmware `baro_valid` flag. Missing or invalid reading returns ValueNotSet, never zero. Old channel IDs unchanged. After install of updated driver and instance restart, Rescan on goastro Switches page finds the two new channels.

Switch actions below.

- `SaveSettings` writes live settings to flash.
- `RestoreDefaults` restores default settings in RAM.
- `Status` returns every reading as JSON.
- `SwitchLayout` returns page layout as JSON, see `layout.go`.

Same process and port also serve `observingconditions/0` on same open board. UniqueID `ScopeHub-oc`, plus `-<serial>` when serial set. Name `Scope Hub Weather`. `"observingconditions": [{"enable": false}]` in device file turns it off. Block may set own `"name"`.

ObservingConditions properties below.

- Temperature from dew controller air temperature, offset applied, same as switch 52.
- Humidity from SHT45.
- DewPoint from dew controller.
- Pressure from BMP581, station pressure, hPa.

Invalid reading returns ValueNotSet. Other properties not implemented. VEML7700 light sensor drives LED dimming, not sky brightness, so ObservingConditions does not report it.

ObservingConditions actions use pi_mount_gps names and formats. Actions are `Temperature`, `Humidity`, `Pressure`, `DewPoint`, `Latitude`, `Longitude`, `Altitude`, `Satellites`, `FixQuality`, `GpsTime`, and `Gps` as JSON `{valid,time,latitude,longitude,altitude,satellites,fixQuality,fixType,pdop,hdop,vdop}`. `valid` needs live NMEA and GGA fix. NEO-M9N report has no GSA, so `fixType` is `""` and dilutions are 0.

License [AGPL-3.0-only](../LICENSE).
