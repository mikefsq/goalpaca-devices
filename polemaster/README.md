# PoleMaster Alpaca driver

Expose a QHY PoleMaster USB camera to ASCOM Alpaca clients on macOS or Linux.
The driver supports monochrome capture, exposure, gain, offset, 8-bit or 12-bit
readout, and subframes. Run it as a standalone server or through alpacahurd.

The underlying Go USB driver includes the bridge firmware and needs no QHY SDK.
This adapter provides the Alpaca camera interface; it does not perform polar alignment.

## Build

Use Go 1.25 or later.

On macOS, install Apple's Command Line Tools and leave cgo enabled. On Linux,
the transport supports builds with `CGO_ENABLED=0`; your user also needs read and
write access to the camera's USB device. See the
[USB driver setup instructions](https://github.com/mikefsq/polemaster#build) for permissions.

From this directory:

```sh
go build -o polemaster ./cmd/polemaster
```

## Run and connect

```sh
./polemaster -port 11125
```

Open `http://localhost:11125/setup`, then select the camera in your Alpaca client
through discovery or by entering the server's address and port. The standalone
camera is device `0`. Without `-port`, the HTTP port is `11111`.

The server starts even if no camera is attached. It retries acquisition in the
background and reloads firmware after a physical replug. The camera is shared by
clients: `Connected` reflects hardware presence, and a client disconnect leaves
the USB handle open. Use one attached PoleMaster; there is no per-unit selector.

To check the HTTP endpoint:

```sh
curl 'http://localhost:11125/api/v1/camera/0/cameraxsize?ClientID=1&ClientTransactionID=1'
```

For capture, connect in your client, choose the readout mode and exposure, and
start an exposure. The client can retrieve the image once `ImageReady` is true.

## Capture settings

| Setting | Supported values |
|---|---|
| Sensor | MT9M034, 1280 × 960 monochrome, 3.75 µm pixels |
| Readout mode `0` | 8-bit, pixel values 0–255; default |
| Readout mode `1` | 12-bit, pixel values 0–4095 |
| Gain | 1–40; steps 1–7 use analog gain, higher steps add digital gain |
| Offset | 0–4045; sensor pedestal is this value plus 50 |
| Binning | 1 × 1 only |
| Shutter, cooling, pulse guiding | Not supported |

Twelve-bit capture preserves all sensor bits and uses two bytes per pixel.
Dark frames require a lens cap; the exposure's `Light` flag is ignored.

Set `StartX`, `StartY`, `NumX`, and `NumY` so the whole subframe fits within the
sensor. Odd coordinates and dimensions are supported. The adapter reads a legal
enclosing sensor window and crops it to the requested region. It prioritizes
fewer rows to reduce readout time, then minimizes width. Full-frame readout takes
about 260 ms; shorter windows can read faster.

## Exposure behavior

The sensor uses 262.5 µs row increments up to about 17.06 seconds. Nonnegative
requests below one row are raised to one row. The advertised maximum of about
4.7 hours is the protocol's numeric limit: the supplied firmware implements the
added portion by delaying pixel transfer, so it is not a verified long-integration
capability. `LastExposureDuration` reports the programmed duration.

Capture discards initial frames so the returned image reflects the new settings.
`PercentCompleted` is an elapsed-time estimate based on three readout periods;
long exposures may remain at 99% until capture completes.

`AbortExposure` marks the result for discard and waits for the active capture to
return. It does not immediately cancel USB I/O. `StopExposure` is unsupported,
and shutdown also waits for capture to finish.

## Configuration

Use `-help` for flags or generate a device file:

```sh
./polemaster -schema commented > camera.json
```

Edit the generated file, then validate and run it:

```sh
./polemaster -config camera.json -check
./polemaster -config camera.json -port 11125
```

The driver-specific key is `firmware`: a path to a custom Intel HEX image, loaded
on each acquisition. Leave it empty to use embedded firmware. See the
[shared configuration guide](../README.md#configuration) for common keys and
alpacahurd setup.

## Tests

Unit tests use a simulated USB device:

```sh
go test ./...
```

To exercise HTTP and USB with a real camera, stop other software using it and
point it at a still scene with visible detail:

```sh
POLEMASTER_HARDWARE=1 go test -run '^TestAlpacaHardware$' -count=1 -v ./...
```

The hardware test changes settings and captures images. `POLEMASTER_FIRMWARE`
selects a custom image; `POLEMASTER_OFFSET_SCAN=1` logs nearby crop comparisons.
Flat scenes cause the crop-placement check to skip.
