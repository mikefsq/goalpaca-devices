//go:build windows

package driver

// Windows HID transport: hid.dll and SetupAPI

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// GUID_DEVINTERFACE_HID
var hidGUID = guid{0x4D1E55B2, 0xF16F, 0x11CF, [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30}}

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10
)

var (
	modSetupapi = syscall.NewLazyDLL("setupapi.dll")
	modHid      = syscall.NewLazyDLL("hid.dll")

	procGetClassDevs       = modSetupapi.NewProc("SetupDiGetClassDevsW")
	procEnumInterfaces     = modSetupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procGetInterfaceDetail = modSetupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procDestroyDeviceList  = modSetupapi.NewProc("SetupDiDestroyDeviceInfoList")

	procGetAttributes     = modHid.NewProc("HidD_GetAttributes")
	procGetSerial         = modHid.NewProc("HidD_GetSerialNumberString")
	procGetProduct        = modHid.NewProc("HidD_GetProductString")
	procSetFeature        = modHid.NewProc("HidD_SetFeature")
	procGetFeature        = modHid.NewProc("HidD_GetFeature")
	procGetPreparsedData  = modHid.NewProc("HidD_GetPreparsedData")
	procFreePreparsedData = modHid.NewProc("HidD_FreePreparsedData")
	procGetCaps           = modHid.NewProc("HidP_GetCaps")
)

type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGuid guid
	flags              uint32
	reserved           uintptr
}

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

func openHandle(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
}

// detailPath runs the two-call SetupDiGetDeviceInterfaceDetail pattern.
func detailPath(h uintptr, ida *spDeviceInterfaceData) string {
	var required uint32
	procGetInterfaceDetail.Call(h, uintptr(unsafe.Pointer(ida)), 0, 0, uintptr(unsafe.Pointer(&required)), 0)
	if required < 6 {
		return ""
	}
	buf := make([]byte, required)
	cb := uint32(8) // sizeof(SP_DEVICE_INTERFACE_DETAIL_DATA_W) on 64-bit
	if unsafe.Sizeof(uintptr(0)) == 4 {
		cb = 6
	}
	*(*uint32)(unsafe.Pointer(&buf[0])) = cb
	r, _, _ := procGetInterfaceDetail.Call(h, uintptr(unsafe.Pointer(ida)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(required), 0, 0)
	if r == 0 {
		return ""
	}
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), (required-4)/2)
	return syscall.UTF16ToString(u16)
}

func hidString(proc *syscall.LazyProc, h syscall.Handle) string {
	var buf [128]uint16
	if r, _, _ := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2)); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func listHID(vid, pid uint16) ([]hidDev, error) {
	h, _, _ := procGetClassDevs.Call(uintptr(unsafe.Pointer(&hidGUID)), 0, 0, digcfPresent|digcfDeviceInterface)
	if h == ^uintptr(0) {
		return nil, errors.New("SetupDiGetClassDevs failed")
	}
	defer procDestroyDeviceList.Call(h)

	var out []hidDev
	var ida spDeviceInterfaceData
	ida.cbSize = uint32(unsafe.Sizeof(ida))
	for i := 0; ; i++ {
		r, _, _ := procEnumInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&hidGUID)), uintptr(i), uintptr(unsafe.Pointer(&ida)))
		if r == 0 {
			break
		}
		path := detailPath(h, &ida)
		if path == "" {
			continue
		}
		dh, err := openHandle(path)
		if err != nil {
			continue
		}
		var a hiddAttributes
		a.Size = uint32(unsafe.Sizeof(a))
		r, _, _ = procGetAttributes.Call(uintptr(dh), uintptr(unsafe.Pointer(&a)))
		if r != 0 && a.VendorID == vid && a.ProductID == pid {
			out = append(out, hidDev{path: path, serial: hidString(procGetSerial, dh), product: hidString(procGetProduct, dh)})
		}
		syscall.CloseHandle(dh)
	}
	return out, nil
}

type windowsHID struct {
	h       syscall.Handle
	featLen int // HIDP_CAPS.FeatureReportByteLength: the largest report plus its ID
}

func openHID(path string) (transport, error) {
	h, err := openHandle(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	t := &windowsHID{h: h, featLen: hidStatusBytes + 1}
	var pp uintptr
	if r, _, _ := procGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r != 0 {
		var caps [64]uint16 // FeatureReportByteLength is the 5th USHORT of HIDP_CAPS
		procGetCaps.Call(pp, uintptr(unsafe.Pointer(&caps[0])))
		if caps[4] != 0 {
			t.featLen = int(caps[4])
		}
		procFreePreparsedData.Call(pp)
	}
	return t, nil
}

// hid.dll wants buffers of the device's full feature length, with the report ID in byte 0.
func (t *windowsHID) getFeature(id byte, n int) ([]byte, error) {
	buf := make([]byte, max(t.featLen, n+1))
	buf[0] = id
	if r, _, err := procGetFeature.Call(uintptr(t.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r == 0 {
		return nil, fmt.Errorf("HidD_GetFeature(%d): %v", id, err)
	}
	return buf[1 : n+1], nil
}

func (t *windowsHID) setFeature(id byte, data []byte) error {
	buf := make([]byte, max(t.featLen, len(data)+1))
	buf[0] = id
	copy(buf[1:], data)
	if r, _, err := procSetFeature.Call(uintptr(t.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r == 0 {
		return fmt.Errorf("HidD_SetFeature(%d): %v", id, err)
	}
	return nil
}

func (t *windowsHID) close() error { return syscall.CloseHandle(t.h) }
