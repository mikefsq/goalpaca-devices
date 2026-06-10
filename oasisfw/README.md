# oasisfw — Oasis filter wheel Alpaca driver

Standalone ASCOM Alpaca **FilterWheel** server for the Astroasis Oasis filter wheel,
over the pure-Go `oasis-astro/oasisfw` library (USB-HID, no vendor SDK).

Positions are 0-based and report -1 while moving (ASCOM convention); slot names and
focus offsets are read from the wheel at connect.

```sh
go run . -port 11123 -wheel 0
```
Flags: `-port`, `-wheel` (index), `-discovery`.
Transport: pure Go on Linux/Windows; macOS HID uses cgo (IOKit), per the library.
