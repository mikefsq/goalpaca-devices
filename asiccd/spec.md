# Per-Device Alpaca Library — Implementation Spec (Go)

*(Working package name `alpacadev` — placeholder; rename freely.)*

## 1. Purpose

A Go library a device author imports to expose one hardware device as a standalone, platform-independent **ASCOM Alpaca** server (HTTP/JSON REST + UDP discovery). The author writes hardware logic against a typed interface and a small hardware-lifecycle interface; the library handles the wire protocol, discovery participation, async semantics, image transport, and liveness.

One binary = one device (or a few behind one port). INDI is **out of scope**—handled, when needed, by separate existing INDI drivers. The two don't coexist in one process (and can't share exclusive USB hardware anyway).

## 2. Non-goals / boundaries

- **No INDI.** Alpaca only. No protocol abstraction, no neutral model.
- **Not an aggregator/proxy.** A library instance serves only its own device(s); never in another device's data path.
- **Not a hardware abstraction layer.** The author writes device I/O behind the typed interface (typically over a vendor SDK).
- **Does not invent device types.** The interop surface is the fixed ASCOM set (§5.2); anything outside it is `Switch` or `Action`.
- **No central dependency.** A device runs fully standalone; the discovery server is optional and affects only findability, never operation.

## 3. Core principles

### 3.1 The process owns the hardware; `Connected` is logical

This is the differentiator. The physical hardware connection (vendor SDK handle, cooling loop, any background regulation) is opened **once at process start** and held for the life of the process—**independent of any Alpaca client**.

Alpaca `Connected` is **not** a hardware open/close — a client connecting or disconnecting MUST NOT open or close the hardware. In these drivers `Connected` is defined as **hardware presence** (`Connected ≡ hwPresent`): a device-level signal answering *"is the device attached and usable?"*, shared across clients. `Connect` is a presence-check handshake (it succeeds iff the hardware is attached, and is the natural point for a client to read device info); `Disconnect` is a **no-op**, so one client disconnecting never disturbs another client or the hardware.

Consequence (the win): a camera stays at cooling setpoint while NINA connects, disconnects, restarts, reconfigures, or crashes. The driver process is the persistent hardware owner. A naive `Connected=true → open / Connected=false → close` mapping would defeat this—do not do it. (For a ZWO camera specifically, a disconnect/reconnect would zero the TEC, which then ramps back over ~10 min — so the persistent owner is mandatory, not just convenient.)

### 3.3 Connectionless access; the device guards its own state

Access is effectively connectionless: any connected client may `GET` (read) freely — a client knows its own connection intent locally, so `Connected` only needs to convey hardware availability. **Writes are arbitrated by device state, not client identity**: while the device is in a transitory state (a camera exposing/reading, a focuser/rotator/wheel moving) it rejects mutating `PUT`s with `InvalidOperation` (`0x40B`), except the interrupt members (`abortexposure`/`stopexposure`/`halt`). This blocks a second client from clobbering an operation in progress with **no** session/ownership/idle-timeout bookkeeping. Recovery of a wedged device is a per-port power cycle, not an Alpaca call.

### 3.2 Standalone & out of the data path

The device serves its own REST directly. Discovery is bootstrap-only. Nothing the library does sits between a client and another device.

## 4. Architecture

```
   author code  --implements-->  alpacadev.Camera   (typed interface: ASCOM members)
                                  alpacadev.Hardware (Open/Close: SDK handle, cooling loop)
                                       |
                              alpacadev.BaseDevice (logical connection state, Op helpers)
                                       |
                    +------------------+-------------------+
                    |                                      |
          HTTP server (Alpaca REST, poll)        Discovery participation (UDP 32227)
          - /management + /api/v1                - Direct: self-answer
          - async initiator + completion         - Register: unicast heartbeat
          - DeviceState batch                    - shared JSON schema
          - ImageBytes (cameras)
          - error mapping
                    |
              Liveness / health
```

Alpaca is **poll-only**, so there is no change-event/publish machinery—the library serves getters on each request. `DeviceState` batches getters into one response.

## 5. Canonical device model

### 5.1 Common `Device` interface

```go
type Device interface {
    // Identity
    UniqueID() string          // stable GUID; keys registration & client identity
    Name() string
    Description() string
    DriverInfo() string
    DriverVersion() string
    InterfaceVersion() int

    // Logical connection (NOT hardware open/close — see 3.1)
    Connect(ctx context.Context) error    // marks this client session connected
    Disconnect(ctx context.Context) error // marks it disconnected; hardware stays up
    Connected() bool

    // Non-standard functionality (the Alpaca escape hatch)
    SupportedActions() []string
    Action(name, params string) (string, error)
    CommandString(cmd string, raw bool) (string, error)
    CommandBool(cmd string, raw bool) (bool, error)
    CommandBlind(cmd string, raw bool) error

    // Batched state snapshot (Alpaca DeviceState)
    DeviceState() []StateValue
}

type StateValue struct {
    Name  string
    Value any
}
```

### 5.2 Hardware lifecycle (the persistent owner)

```go
// If a device implements Hardware, Open is called once when the server Runs,
// and Close once at graceful shutdown. The handle lives for the whole process.
type Hardware interface {
    Open(ctx context.Context) error   // open SDK, start cooling loop / regulation
    Close(ctx context.Context) error  // release on shutdown only
}
```

The author's `Connect`/`Disconnect` do logical bookkeeping only. Background regulation (cooling) runs in a goroutine started by `Open` and stopped by `Close`, never by client connect state.

### 5.3 Per-type interfaces

Each embeds `Device` and adds exactly the ASCOM members for that type. Fixed set: `Camera`, `CoverCalibrator`, `Dome`, `FilterWheel`, `Focuser`, `ObservingConditions`, `Rotator`, `SafetyMonitor`, `Switch`, `Telescope`. The **ASCOM Master Interface Definitions** are authoritative for member names, types, and async classification.

```go
type Camera interface {
    Device
    StartExposure(duration float64, light bool) error // initiator
    ImageReady() bool                                  // completion property
    AbortExposure() error
    ImageBytes() (ImageFrame, error)                   // binary transport (6.4)
    CameraXSize() int
    CameraYSize() int
    SetCCDTemperature(c float64) error                 // setpoint -> driver-owned policy
    CCDTemperature() float64
    CoolerOn() bool
    SetCoolerOn(bool) error
    // ... full Camera member set
}
```

### 5.4 Async operation pattern

ASCOM async = initiator returns quickly + completion property polled. Library helper:

```go
type Op struct{ /* Idle | Busy | Done | Failed (+ error) */ }
func (o *Op) Begin()
func (o *Op) Complete()
func (o *Op) Fail(err error)
func (o *Op) Busy() bool   // backs Slewing / ImageReady-inverse / Connecting
```

Initiators (`StartExposure`, `SlewToCoordinatesAsync`, `Connect`) call `Begin`, run work in a goroutine, return immediately; the completion property reads `Op`. Connect/Disconnect use this (`Connecting` property) per Platform 7, while still honoring the legacy `Connected` set.

## 6. HTTP server (Alpaca adapter)

### 6.1 Routing

- Device API: `GET|PUT /api/v1/{device_type}/{device_number}/{member}`
- Management: `GET /management/apiversions`, `/management/v1/description`, `/management/v1/configureddevices`
- Optional setup: `GET /setup`, `/setup/v1/{type}/{num}/setup`

GET → getter; PUT (form-encoded) → setter / async initiator. Member path segments and **parameter names are case-insensitive**.

### 6.2 Envelope & transaction IDs

```json
{ "Value": <result>, "ClientTransactionID": 0, "ServerTransactionID": 0,
  "ErrorNumber": 0, "ErrorMessage": "" }
```

- Echo `ClientTransactionID` and `ClientID` (case-insensitive params).
- `ServerTransactionID`: monotonic per-server counter.
- Driver errors returned in-band (HTTP 200, non-zero `ErrorNumber`); transport/parse failures are HTTP 4xx/5xx.

### 6.3 Async & DeviceState

- An async initiator PUT returns immediately with `ErrorNumber: 0`; client polls the completion property.
- `GET .../devicestate` returns the batched `DeviceState()` snapshot to collapse N polls into one round-trip.

### 6.4 Camera image transport

- Implement **ImageBytes** (binary, `Accept: application/imagebytes`): a fixed metadata header (rank, dimensions, element type, transmission type) + raw little-endian pixels.
- For a sensor like the ASI6200 (~61 MP × 16-bit ≈ 120 MB/frame) ImageArray-JSON is unusable; ImageBytes is mandatory. Provide ImageArray only as a last-resort fallback for clients that demand it.

```go
type ImageFrame struct {
    Rank        int     // 2 (mono) or 3 (color planes)
    Width, Height int
    ElementType int     // e.g. Int32 per the ImageBytes type codes
    Pixels      []byte  // raw little-endian
}
```

### 6.5 Error mapping

Go error → ASCOM error number. Confident set:

| Error                | Number      |
|----------------------|-------------|
| NotImplemented       | 0x400       |
| InvalidValue         | 0x401       |
| ValueNotSet          | 0x402       |
| NotConnected         | 0x407       |
| Driver-defined range | 0x500–0xFFF |

Provide sentinels (`alpacadev.ErrNotImplemented`, …) and a driver-code wrapper. *Confirm the full reserved table against the Alpaca API reference (§12).*

## 7. Discovery participation

Two modes, one shared JSON schema (matches the discovery-server spec):

```json
{ "AlpacaPort": 11111, "UniqueID": "...", "DeviceType": "Camera", "DeviceName": "ASI6200" }
```

- **Direct** (device on its own IP): bind UDP `32227`, answer `alpacadiscovery` requests with `{"AlpacaPort": <port>}`. Zero server dependency.
- **Register** (device sharing the discovery server's IP): send the schema above as a **periodic unicast heartbeat** to `Discovery.ServerAddr:32227` at `Discovery.Interval` (≈ TTL/3). Never broadcast.
- **Off**: no discovery; reached by manual IP:port only.

One JSON emitter serves direct and register; only trigger and destination differ.

## 8. Liveness / health

- The registration heartbeat doubles as the liveness signal the discovery server tracks.
- Expose an internal health view (hardware open, cooling at setpoint, last error, in-flight ops). Optional local `GET /health` for a supervisor/watchdog.
- Health never gates the data path; supervised restart is the recovery model.

## 9. Concurrency & lifecycle

- On `Run`: call `Hardware.Open` once (opens SDK, starts regulation goroutine), start the HTTP server, start discovery (responder or heartbeat ticker).
- Logical connection state + `Op` transitions are mutex-guarded; HTTP handlers and hardware goroutines both touch device state.
- Background regulation (cooling) runs independent of client connection state for the life of the process.
- `context.Context` drives graceful shutdown: drain HTTP, stop heartbeat, then `Hardware.Close` last so cooling persists until the very end.
- **Mid-exposure client loss:** if the connected client disconnects/crashes during an exposure, the driver decides policy—abort cleanly and remain at setpoint. The hardware can never be left wedged because the driver, not the client, owns it.

## 10. Public API surface

```go
type Config struct {
    AlpacaPort int                 // e.g. 11111
    Discovery  DiscoveryConfig
}

type DiscoveryConfig struct {
    Mode       DiscoveryMode       // Direct | Register | Off
    ServerAddr string              // host:32227, for Register
    Interval   time.Duration
}

type Server struct{ /* ... */ }

func New(cfg Config) *Server
func (s *Server) Register(devType DeviceType, number int, d Device) error
func (s *Server) Run(ctx context.Context) error   // calls Hardware.Open, serves, Hardware.Close on exit
```

Author usage:

```go
type ASI6200 struct {
    alpacadev.BaseDevice
    sdk *asi.Camera // your Go SDK wrapper
}
// implement alpacadev.Camera members + alpacadev.Hardware (Open/Close)

func (c *ASI6200) Open(ctx context.Context) error {
    if err := c.sdk.OpenAndInit(); err != nil { return err }
    go c.coolingLoop(ctx) // persists for process life
    return nil
}
func (c *ASI6200) Connect(ctx context.Context) error    { c.BaseDevice.MarkConnected(); return nil } // logical only
func (c *ASI6200) Disconnect(ctx context.Context) error { c.BaseDevice.MarkDisconnected(); return nil }

func main() {
    s := alpacadev.New(alpacadev.Config{
        AlpacaPort: 11111,
        Discovery:  alpacadev.DiscoveryConfig{Mode: alpacadev.Register, ServerAddr: "discovery.local:32227", Interval: 10 * time.Second},
    })
    _ = s.Register(alpacadev.CameraType, 0, &ASI6200{})
    log.Fatal(s.Run(context.Background()))
}
```

## 11. Configuration

| Field                | Default   | Notes                                          |
|----------------------|-----------|------------------------------------------------|
| `AlpacaPort`         | —         | The device's HTTP REST port.                   |
| `Discovery.Mode`     | `Register`| `Direct` if device owns its IP.                |
| `Discovery.ServerAddr`| —        | `host:32227` for Register mode.                |
| `Discovery.Interval` | `10s`     | Heartbeat cadence (≈ discovery-server TTL/3).  |

## 12. Open items / to verify

1. **Full ASCOM error-number table** — confirm the complete reserved set against the Alpaca API reference.
2. **ImageBytes header layout** — implement to the exact field order/types and element-type codes in the ImageBytes spec.
3. **Per-member async classification** — mark initiators vs immediate per the Master Interface Definitions; wrong classification is invisible locally but breaks clients over the network.
4. **Multi-client policy** — *Resolved (§3.3).* No reference counting and no per-client sessions: `Connected ≡ hardware presence` (device-level), and write conflicts are prevented by **state-based gating** (mutating PUTs rejected while the device is Busy/transitory), not by client ownership. Reads are open to all clients; the driver stays the source of truth for persistent regulation (cooling setpoint).
5. **Package name** — `alpacadev` is a placeholder.
