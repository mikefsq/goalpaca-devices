# Driver setup forms

Return a pointer to a tagged config struct from `registry.Driver.Config`.
`devicemain` uses it for command-line flags and the browser setup page.

```go
type Config struct {
    Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start"`
    Speed  int    `json:"speed,omitempty" alpaca:"label=Speed,min=1,max=100"`
}
```

Use `when=start` for hardware selectors and settings without a live setter.
Those fields are read-only in the browser; users change the config file and
reload or restart. Other fields are live by default and require the device
to implement `Reconfigure(any) error`.

`Reconfigure` receives a fresh config pointer containing current values plus
the submission. Validate all changes before applying them, and synchronize
with device operations. Returning an error rejects the submission but does
not undo hardware changes already made.

File and flag values are pinned by the host. Editable settings are persisted
by the framework. Tags do not replace constructor validation for config files.

Use `alpaca:"hidden"` for fields such as lists that the generated form cannot
represent. For example, MGPBox's `feed` targets are configured in the file.

See [astrocam/hurd.go](astrocam/hurd.go) and
[astrocam/astrocam_test.go](astrocam/astrocam_test.go) for a working implementation.
The [goalpaca setup-form reference](https://github.com/mikefsq/goalpaca/blob/main/SETUP_FORMS.md)
documents all tags, validation, persistence, and custom forms.
