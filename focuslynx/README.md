# focuslynx — Optec FocusLynx / ThirdLynx Alpaca driver

Standalone ASCOM Alpaca **Focuser** server for one channel of an Optec FocusLynx /
ThirdLynx hub, over the pure-Go `optec-astro/focuslynx` library (USB-serial).

FocusLynx has two ports (F1/F2); run a second instance with `-channel 2` and a
different `-port`. ThirdLynx is single-channel (F1).

```sh
go run . -port 11122 -hub 0 -channel 1
```
Flags: `-port`, `-hub` (index), `-channel` (1|2), `-discovery`. Builds CGO-free for any target.
