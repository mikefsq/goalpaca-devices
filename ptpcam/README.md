# ptpcam

A Fujifilm or Sony stills camera as an ASCOM Alpaca camera, over
[github.com/mikefsq/ptp](https://github.com/mikefsq/ptp).

    make ptpcam
    ./bin/ptpcam -list
    ./bin/ptpcam                      # Alpaca on :11125

## These are not astro CMOS cameras, but this driver returns a raw ImageFrame like astro CMOS drivers.

Two consequences worth knowing before you use it:

- **`LastExposureDuration` reports the actual exposure, not the requested exposure.** 
- **A dial in a marked position might prevent writing.** Writes to a setting the camera owns are
  accepted and silently ignored — by the *camera*, not by this driver. Put the
  Fujifilm shutter dial on **T**, the ISO dial on **C**, and the focus lever on
  **M**; a Sony wants **PC Remote** USB mode and the mode dial on **M**.

## How Image Data is transferred

Image data is delivered through an Alpaca request.

A RAW capture is decoded to the **undemosaiced sensor readout** and delivered as
a Rank-2, 16-bit `ImageFrame`: one sample per photosite at the full readout 
geometry rather than the vendor's crop.


### A capture is three steps, and the third is not optional

    capture -> fetch -> delete  cf card; and the frame becomes ImageFrame
    capture -> skip  -> delete  cf-card; the no bytes over USB interface

**A pending Fujji frame blocks the camera** While one sits in a Fujifilm
body's volatile store it answers `RefusedRightNow` to property writes. 
Reading the frame does not clear it, so a delete is required. The image
is written to cf-card immediately so a delete without a download is how to 
achieve the fastest fps.

### A frame that will not decode

A RAW variant this build cannot unpack. e.g. Fujifilm's lossy mode, 
leaves `ImageReady` false. The file will have to be fetched from the cf-card.

## It comes up without a camera

The Alpaca endpoint starts whether or not a body is attached. The driver
acquires the camera when one appears and re-acquires it after a power cycle.
`Connected` reports actual hardware presence, so a client will need to poll
this value to see the underlying hardware state. 

Three kinds of non-responsive camera states are possible:

- **Physical absence** — unplugged or powered off. Caught by a probe over the OS
  USB registry that **never touches the open camera**.
- **A wedged session** — still enumerated, but no longer answering. The presence
  probe cannot see this; `ptp.ErrNotResponding` is the only signal, and it forces
  a re-acquisition.
- **Busy** A camera *refusing* an operation is not a failure. A body may report 
  "not now" for various reasons like a dial set incorrectly or if it is still 
   writing to cf-card.


## Vendor-neutral

The driver holds the parent package's capability interfaces like `ptp.Capturer`,
`ExposureControl`, `Downloader`, `FocusControl`, `LiveViewer`. The Vendor libraray 
implements the vendor specific types and mapping to the standard types that Alpaca
makes accessable on the network.
