# ptpcam

ASCOM Alpaca camera driver for Fujifilm and Sony USB PTP cameras, using the
[ptp](https://github.com/mikefsq/ptp) library.

## Build and run

From the repository root:

```sh
make ptpcam
./bin/ptpcam -list
./bin/ptpcam -vendor fuji -pixelSize 3.04
```

The HTTP port defaults to `11111`. Use `-serial` to select a body, and set
`-pixelSize` to its photosite pitch in micrometres. If the camera does not
report its dimensions, supply both `-sensorWidth` and `-sensorHeight` in
pixels. Use values for your camera's full sensor readout.

See `-help` and the [shared configuration instructions](../README.md#configuration).

## Camera setup

For Fujifilm, put the shutter dial on T, the ISO dial on C, and the focus
lever on M. For Sony, use PC Remote USB mode and manual exposure mode.
A camera may ignore writes to settings controlled by its physical dials.
`LastExposureDuration` reports the actual exposure duration.

RAW captures are decoded to undemosaiced, 16-bit sensor samples. Unsupported
RAW encodings leave `ImageReady` false; use a RAW mode supported by the PTP
library. A pending Fujifilm capture is released after transfer so it does
not block subsequent settings or exposures.

The server starts without a camera attached and reacquires it after a power
cycle. `Connected` reports hardware availability. A busy response can also
mean the camera is writing a capture or a physical control prevents the
operation.
