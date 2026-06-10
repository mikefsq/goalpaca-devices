# oasisfoc — Oasis focuser Alpaca driver

Standalone ASCOM Alpaca **Focuser** server for the Astroasis Oasis focuser, over the
pure-Go `oasis-astro/oasisfoc` library (USB-HID, no vendor SDK).

Absolute focuser (encoder + MoveTo/Position); it also has a manual clutch, so the
reported position can change without a commanded move — the encoder still tracks it.

```sh
go run . -port 11120 -focuser 0
```
Flags: `-port`, `-focuser` (index), `-discovery direct|register|off`.
Transport: pure Go on Linux/Windows; macOS HID uses cgo (IOKit), per the library.
