# Deep Sky Dad FP2-family panels

This driver lets Alpaca clients control a Deep Sky Dad flat panel as a CoverCalibrator device. It
runs on Linux (including Raspberry Pi), macOS and Windows, and you don't need the vendor's control
panel application.

The driver talks to the panel over USB serial at 115200 baud.

## Supported models

The driver asks the panel what it is when it connects.

| Product ID | Model | What the driver controls |
|---|---|---|
| 1 | FP2 | Cover and light |
| 2 | FT2 and compatible light-only panels | Light |
| 3 | OFP2 | Cover and light |
| 4 | FP2+ | Cover and light |
| 5 | SFP2 | Cover |

The driver rejects FP1 and unknown product IDs. A newer model needs a matching product ID and the
same protocol, and it should be tested on real hardware before you rely on it.

## Requirements

- **Linux:** the account running the driver usually needs to be in the `dialout` group.
- **macOS:** build with cgo. You also need Apple's command-line tools: `xcode-select --install`.
- **Windows:** no extra requirements.

## Build and install

From the repository root:

```sh
make deepskydadfp
./bin/deepskydadfp -schema commented
./bin/deepskydadfp -discover
sudo make -C deepskydadfp install
```

Installing adds the driver to alpacahurd's `drivers.conf`. Then add a device from alpacahurd's
setup page, or create a JSON file like this:

```json
{
  "driver": "deepskydadfp",
  "serial": "YOUR-USB-SERIAL",
  "enable": false
}
```

Check the file without touching any hardware, set `enable` to `true`, and then start the driver:

```sh
./bin/deepskydadfp -config panel.json -check
./bin/deepskydadfp -config panel.json
```

## Choosing the panel

There are three ways to select a panel:

| Setting | What happens |
|---|---|
| `serial` | Finds the panel with this USB serial number. This is the best choice, because it doesn't change between ports or reboots. |
| `path` | Uses this serial port, such as `/dev/serial/by-id/YOUR-PANEL` or `COM5`. This overrides `serial`. |
| Neither | Finds the panel automatically. This only works when exactly one panel is connected. |

The `port` setting is different: it's the Alpaca HTTP port, not the panel's serial port.

Automatic detection looks for a USB device named `Deep Sky Dad FP2`. Windows sometimes reports only
a generic name, such as `USB Serial Device (COM5)`. In that case the driver matches the USB IDs
`2E8A:000A` instead. Other Pico boards share these IDs, so one could appear in the discovery list on
Windows. It won't connect, because it fails to identify itself as an FP2.

Discovery only reads USB information and never opens a port, so a panel in use by another program
is still listed.

If the driver can't find your panel, set `path`:

```json
{
  "driver": "deepskydadfp",
  "path": "/dev/serial/by-id/YOUR-PANEL",
  "enable": false
}
```

## How the driver behaves

### Light

Brightness is a whole number from 0 to 4096.

`CalibratorOn` sets the brightness and turns the light on. The firmware won't light the panel while
the cover is open, so on models with a cover, the driver closes the cover first and turns the light
on once the cover is confirmed closed. ASCOM allows this.

`CalibratorOff` turns the light off and leaves the cover alone.

A pending light-on is cancelled by `CalibratorOff`, `HaltCover`, a client disconnecting, or the
cover failing to close.

### Cover

`OpenCover` and `CloseCover` return immediately. `CoverState` reports `Moving` until the cover
reaches its target.

`HaltCover` stops the cover. `CoverState` then reports `Unknown` until the next position reading.

### Waiting and errors

Commands return without waiting for the hardware to finish. The driver checks the panel every
500 ms.

| Situation | What clients see |
|---|---|
| Light still changing | `CalibratorChanging` is true, and `CalibratorState` is `NotReady` |
| Light not confirmed within 5 seconds | `CalibratorError` |
| Cover stops early, or takes over 90 seconds | `CoverError` |
| Panel data is out of date | `Unknown` |
| A read or command fails | `Error` |

An error stays visible until the next operation clears it.

Operations a model doesn't support return `NotImplemented`. On models with no light, `Brightness`
and `MaxBrightness` return `NotImplemented`, and `DeviceState` leaves out `Brightness`.

### Connection

Connecting only reads the panel's identity and status. It doesn't change calibration, heater
settings, the light or the cover.

If the USB connection is lost, the driver reconnects to the panel by itself, but the client must
connect again.

The driver never resends a command when it can't tell whether the panel received it, so a command
can't run twice.

Stopping the driver closes the connection. The panel stays in its current state.

## Custom Alpaca actions

Send actions with a PUT request. Action names aren't case-sensitive, and results are strings in the
normal Alpaca `Value` field. Empty parameters read a setting, and a value writes it.

```sh
curl -X PUT 'http://HOST:PORT/api/v1/covercalibrator/0/action' \
  --data-urlencode 'Action=HeaterTemperature' --data-urlencode 'Parameters='
curl -X PUT 'http://HOST:PORT/api/v1/covercalibrator/0/action' \
  --data-urlencode 'Action=HeaterMode' --data-urlencode 'Parameters=2'
```

### Heater actions

These are available on FP2, OFP2 and FP2+, because FT2 and SFP2 have no heater.

| Action | Parameters | Result |
|---|---|---|
| HeaterTemperature | None | Temperature in degrees Celsius |
| HeaterMode | Empty reads. 0, 1 or 2 sets. | The mode: 0 off, 1 on, 2 on when the cover is open. A set reads it back from the panel. |

A missing sensor or a bad reply returns an error. Setting `HeaterMode` fails if the panel ignores the
command.

## Direct control

To control the panel without an Alpaca server, use `dsdctl` from the `deepskydad` project.

## Testing status

Automated tests use a simulated panel. They cover all five product IDs, HTTP errors, `DeviceState`,
confirming commands, ignored commands, timeouts, cancelled operations and reconnecting.

On a real OFP2, the driver has identified the panel and read its status.
Cover and light commands through this driver haven't run on a real panel yet.

The driver hasn't been through ConformU, and not every model has been tested on hardware. For the
standard it follows, see the
[ASCOM CoverCalibrator V2 specification](https://ascom-standards.org/newdocs/covercalibrator.html).

## Vendor driver notes

We unpacked the vendor's `ASCOM DSD FP Setup v1.0.3.6.exe` without running it and read its .NET code
(assembly `ASCOM.DeepSkyDad.FP.CoverCalibrator`, version 1.0.3.22767, DLL SHA-256
`a260a84d77d0262cbd16355b85dc55fd0f6a1e366572f53f01088c9ba22951a3`).

| Vendor member | What it does |
|---|---|
| SupportedActions | Returns an empty list |
| Action | Always throws an unsupported-action exception |
| CommandString(string, bool) | Throws NotImplemented |
| CommandBlind(string, bool) | Throws NotImplemented |
| GetHeaterTemperature() | Reads `GHTT` as a decimal |
| GetInstalledFirmwareVersion() | Reads `GFRM` |
| GetFirmwareVersion() | Returns the supported firmware versions |

The vendor driver has no custom actions to copy. Its setup dialog uses `GetHeaterTemperature`, but
its heater-mode and heater-power controls send nothing to the panel.

The actions in this driver are new. It doesn't allow raw serial commands.

## Protocol sources

Commands are framed as `[request]` and replies as `(response)`. A successful write replies `(OK)`.

We checked the protocol against the vendor's FP2 Control Panel v1.0.15 (`src/index.js` inside
`resources/app.asar`), and checked framing and motion commands against the
[INDI Deep Sky Dad driver](https://github.com/indilib/indi/blob/master/drivers/auxiliary/deepskydad_fp.cpp).
This driver uses only the protocol, and it contains no vendor code.

For panel setup and calibration, see the
[vendor manuals](https://shop.deepskydad.com/software-and-documentation/).

## License

[AGPL-3.0-only](../LICENSE).
