# sim

A simulated telescope and guide camera sharing one sky model. Guide pulses
move the camera's star field, allowing a client such as PHD2 to calibrate and
guide without hardware.

From the repository root:

```sh
make sim
./bin/sim
```

The server exposes telescope 0 and camera 0 on port `11110`.
Use `-focal-length` (mm), `-pixel-size` (µm), `-width`, and `-height` to set
the guide camera's image scale and dimensions. Run `./bin/sim -help` for all
options.

`make alpacasim` runs goalpaca's separate protocol simulator, which provides
one device of every Alpaca type. Its camera is not coupled to guide pulses.
