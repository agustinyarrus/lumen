package main

// winapi.go — punteros COM/Win32 que el host frameless necesita ademas de los de main.go:
//   * pickImage: dialogo nativo moderno (IFileOpenDialog). Es Per-Monitor-DPI-aware, asi que
//     se ve nitido bajo el contexto DPI v2 que pide WebView2 (el comdlg32 clasico se rompia ahi).
//   * monitorRect / SetWindowPlacement: para el modo pantalla completa real (tapa la barra de tareas).

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ptrSize = unsafe.Sizeof(uintptr(0))

var (
	ole32 = windows.NewLazySystemDLL("ole32.dll")

	pCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	pCoUninitialize   = ole32.NewProc("CoUninitialize")
	pCoCreateInstance = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	pMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW    = user32.NewProc("GetMonitorInfoW")
	pSetWindowPlacement = user32.NewProc("SetWindowPlacement")
)

// --- GUID ---------------------------------------------------------------

type guid struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

func newGUID(d1 uint32, d2, d3 uint16, d4 ...byte) *guid {
	g := &guid{d1: d1, d2: d2, d3: d3}
	copy(g.d4[:], d4)
	return g
}

var (
	clsidFileOpenDialog = newGUID(0xDC1C5A9C, 0xE88A, 0x4DDE, 0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7)
	iidFileOpenDialog   = newGUID(0xD57C7288, 0xD4AD, 0x4768, 0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60)
)

// --- llamada a metodo de vtable COM ------------------------------------

func comCall(this uintptr, idx int, a ...uintptr) int32 {
	vtbl := *(*uintptr)(unsafe.Pointer(this))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*ptrSize))
	args := append([]uintptr{this}, a...)
	r, _, _ := syscall.SyscallN(fn, args...)
	return int32(r)
}

func comRelease(this uintptr) {
	if this != 0 {
		comCall(this, 2) // IUnknown::Release
	}
}

func utf16Ptr(s string) uintptr {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	return uintptr(unsafe.Pointer(p))
}

func utf16PtrToString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var buf []uint16
	for i := uintptr(0); ; i += 2 {
		c := *(*uint16)(unsafe.Pointer(p + i))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return windows.UTF16ToString(buf)
}

type comdlgFilterSpec struct {
	name uintptr
	spec uintptr
}

// pickImage abre el dialogo nativo "Abrir" filtrado a imagenes y devuelve la ruta absoluta
// elegida (o "" si se cancela). Debe correr en el hilo de UI (STA que ya inicializo WebView2).
func pickImage(owner uintptr) string {
	const (
		coinitApartment = 0x2
		clsctxInproc    = 0x1
		fosForceFS      = 0x40       // FOS_FORCEFILESYSTEM
		sigdnFilePath   = 0x80058000 // SIGDN_FILESYSPATH

		mShow         = 3  // IModalWindow::Show
		mSetFileTypes = 4  // IFileDialog::SetFileTypes
		mSetOptions   = 9  // IFileDialog::SetOptions
		mGetOptions   = 10 // IFileDialog::GetOptions
		mSetTitle     = 17 // IFileDialog::SetTitle
		mGetResult    = 20 // IFileDialog::GetResult
		mGetDispName  = 5  // IShellItem::GetDisplayName
	)

	hr := comInit(coinitApartment)
	didInit := hr == 0 // S_OK: nosotros inicializamos -> nos toca CoUninitialize
	if didInit {
		defer pCoUninitialize.Call()
	}

	var dlg uintptr
	r, _, _ := pCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsidFileOpenDialog)), 0, clsctxInproc,
		uintptr(unsafe.Pointer(iidFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if int32(r) < 0 || dlg == 0 {
		return ""
	}
	defer comRelease(dlg)

	var opts uint32
	comCall(dlg, mGetOptions, uintptr(unsafe.Pointer(&opts)))
	comCall(dlg, mSetOptions, uintptr(opts|fosForceFS))

	specs := []comdlgFilterSpec{
		{utf16Ptr("Imágenes"), utf16Ptr("*.jpg;*.jpeg;*.jpe;*.jfif;*.jif;*.png;*.apng;*.gif;*.webp;*.avif;*.bmp;*.dib;*.ico;*.cur;*.svg;*.tif;*.tiff")},
		{utf16Ptr("Todos los archivos"), utf16Ptr("*.*")},
	}
	comCall(dlg, mSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	comCall(dlg, mSetTitle, utf16Ptr("Abrir imagen"))

	if comCall(dlg, mShow, owner) < 0 {
		return "" // cancelado por el usuario (ERROR_CANCELLED)
	}

	var item uintptr
	if comCall(dlg, mGetResult, uintptr(unsafe.Pointer(&item))) < 0 || item == 0 {
		return ""
	}
	defer comRelease(item)

	var psz uintptr
	if comCall(item, mGetDispName, sigdnFilePath, uintptr(unsafe.Pointer(&psz))) < 0 || psz == 0 {
		return ""
	}
	path := utf16PtrToString(psz)
	pCoTaskMemFree.Call(psz)
	return path
}

func comInit(mode uintptr) int32 {
	r, _, _ := pCoInitializeEx.Call(0, mode)
	return int32(r)
}

// --- monitor / pantalla completa ---------------------------------------

type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

// monitorRect devuelve el rectangulo COMPLETO del monitor de la ventana (incluye la barra de
// tareas) para que la pantalla completa la tape.
func monitorRect(hwnd uintptr) rect {
	const monitorDefaultToNearest = 2
	hmon, _, _ := pMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
	if mi.rcMonitor.right == 0 && mi.rcMonitor.bottom == 0 {
		// fallback: pantalla primaria
		return rect{0, 0, sysMetric(smCXSCREEN), sysMetric(smCYSCREEN)}
	}
	return mi.rcMonitor
}

// workAreaForWindow devuelve el área ÚTIL (sin barra de tareas) del monitor donde está la ventana.
// Lo usa el ajuste de ventana-a-imagen para no taparse con la barra de tareas ni salirse del monitor.
func workAreaForWindow(hwnd uintptr) rect {
	const monitorDefaultToNearest = 2
	hmon, _, _ := pMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
	if mi.rcWork.right == 0 && mi.rcWork.bottom == 0 {
		return primaryWorkArea()
	}
	return mi.rcWork
}
