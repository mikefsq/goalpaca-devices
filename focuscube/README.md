# focuscube — Pegasus FocusCube Alpaca driver

Standalone ASCOM Alpaca **Focuser** server for Pegasus Astro FocusCube/FocusCube2/
SMFC/DMFC/ScopsOAG, over the pure-Go `pegasusAstro/focuscube` library (FTDI serial).

The FocusCube does not report a travel limit, so `MaxStep` is host-configured.

```sh
go run . -port 11121 -focuser 0 -maxstep 100000
```
Flags: `-port`, `-focuser` (index), `-maxstep`, `-discovery`. Builds CGO-free for any target.
