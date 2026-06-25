package main

// Lumen — visor de imagenes ultraminimalista, dark, frameless.
//
// Un solo .exe: embebe la UI (carpeta ui/) y la sirve por un server HTTP local a una
// ventana WebView2 SIN marco del sistema. La barra de titulo y los botones min/max/cerrar
// los dibuja la pagina; aca exponemos el puente JS -> Win32 (mover, redimensionar, botones,
// pantalla completa) y servimos los bytes de cada imagen desde el disco.
//
// Frameless (igual que el host de IA History Reader): subclasamos el WndProc y devolvemos 0
// en WM_NCCALCSIZE para que el area cliente ocupe toda la ventana; drag/resize via
// WM_NCLBUTTONDOWN (mantiene Aero Snap).

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	// decodificadores que el navegador NO trae: se decodifican en Go y se reencodean a PNG.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
)

var debugLog = os.Getenv("LUMEN_DEBUG") != ""
var startTime = time.Now()

func dlog(args ...any) {
	if debugLog {
		ms := fmt.Sprintf("[lumen +%dms]", time.Since(startTime).Milliseconds())
		fmt.Fprintln(os.Stderr, append([]any{ms}, args...)...)
	}
}

//go:embed ui
var uiFS embed.FS

const (
	wmNCCALCSIZE     = 0x0083
	wmNCLBUTTONDOWN  = 0x00A1
	wmCLOSE          = 0x0010
	wmERASEBKGND     = 0x0014
	whCBT            = 5
	hcbtCREATEWND    = 3
	smCXSCREEN       = 0
	smCYSCREEN       = 1
	htCAPTION        = 2
	htLEFT           = 10
	htRIGHT          = 11
	htTOP            = 12
	htTOPLEFT        = 13
	htTOPRIGHT       = 14
	htBOTTOM         = 15
	htBOTTOMLEFT     = 16
	htBOTTOMRIGHT    = 17
	swSHOW           = 5
	swMINIMIZE       = 6
	swMAXIMIZE       = 3
	swRESTORE        = 9
	swSHOWMAXIMIZED  = 3
	swSHOWMINIMIZED  = 2
	smCXFRAME        = 32
	smCYFRAME        = 33
	smCXPADDEDBORDER = 92
	swpFRAMECHANGED  = 0x0020
	swpNOMOVE        = 0x0002
	swpNOSIZE        = 0x0001
	swpNOZORDER      = 0x0004
	swpSHOWWINDOW    = 0x0040

	hwndTop      = 0
	hwndTopmost  = ^uintptr(0)     // (HWND)-1
	hwndNoTopmst = ^uintptr(0) - 1 // (HWND)-2
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pSetWindowSubclass        = comctl32.NewProc("SetWindowSubclass")
	pDefSubclassProc          = comctl32.NewProc("DefSubclassProc")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pShowWindow               = user32.NewProc("ShowWindow")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	pGetWindowPlacement       = user32.NewProc("GetWindowPlacement")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pFillRect                 = user32.NewProc("FillRect")
	pCreateSolidBrush         = gdi32.NewProc("CreateSolidBrush")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pSetWindowsHookExW        = user32.NewProc("SetWindowsHookExW")
	pUnhookWindowsHookEx      = user32.NewProc("UnhookWindowsHookEx")
	pCallNextHookEx           = user32.NewProc("CallNextHookEx")
	pGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
	pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	pDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	pSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")

	// onShow lo setea main() una vez creada la ventana: lo invoca el handler /api/show
	// cuando otra invocación de lumen.exe le pasa una imagen (instancia única).
	onShow func(string)

	darkBrush  uintptr
	subclassCB uintptr // callback de subclassProc; lo instala el CBT hook al nacer la ventana
	uiScale    = 1.0   // factor DPI (lo setea main); lo usan los helpers de ajuste de ventana
	fullscreen bool
	savedPlc   windowPlacement
	savedOK    bool

	// Posición final (centrada, px físicos) con la que NACE la ventana. La setea main() antes de
	// crearla y la aplica cbtProc sobre el CREATESTRUCT, para que el primer pixel ya esté centrado
	// y showWin no tenga que moverla -> sin "salto".
	spawnX, spawnY int32
	spawnPosSet    bool

	// Anti-flash: ya NO spawneamos fuera de pantalla (eso dejaba el documento "hidden" -> rAF en
	// pausa). La ventana nace EN pantalla, frameless + oscura desde el 1er pixel (el CBT hook la
	// subclasa al crearse) y recién se revela al pintar (showWin). offscreen queda como escape de debug.
	offscreenSpawn = os.Getenv("LUMEN_OFFSCREEN") != ""
)

type rect struct{ left, top, right, bottom int32 }
type point struct{ x, y int32 }
type nccalcsizeParams struct {
	rgrc  [3]rect
	lppos uintptr
}
type windowPlacement struct {
	length           uint32
	flags            uint32
	showCmd          uint32
	ptMinPosition    point
	ptMaxPosition    point
	rcNormalPosition rect
}

func sysMetric(i int) int32 {
	r, _, _ := pGetSystemMetrics.Call(uintptr(i))
	return int32(r)
}

type createstructW struct {
	lpCreateParams uintptr
	hInstance      uintptr
	hMenu          uintptr
	hwndParent     uintptr
	cy, cx, y, x   int32
	style          int32
	lpszName       uintptr
	lpszClass      uintptr
	dwExStyle      uint32
}
type cbtCreatewnd struct {
	lpcs            uintptr
	hwndInsertAfter uintptr
}

func u16ptrToString(p uintptr) string {
	if p == 0 {
		return ""
	}
	buf := make([]uint16, 0, 24)
	for i := uintptr(0); ; i += 2 {
		c := *(*uint16)(unsafe.Pointer(p + i))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return windows.UTF16ToString(buf)
}

// cbtProc engancha el nacimiento de NUESTRA ventana (CBT hook, corre dentro de CreateWindowEx, antes
// del ShowWindow que hace go-webview2): la subclasa al instante para que nazca frameless + oscura, y
// le fija en el CREATESTRUCT la posición final (centrada). go-webview2 sólo expone Center (no X/Y) y
// muestra la ventana enseguida; sin esto se vería un flash de barra de título nativa + fondo claro
// mientras WebView2 hace su cold-start. (offscreen = -32000 queda como escape de debug.)
func cbtProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) == hcbtCREATEWND && lParam != 0 {
		cbt := (*cbtCreatewnd)(unsafe.Pointer(lParam))
		if cbt.lpcs != 0 {
			cs := (*createstructW)(unsafe.Pointer(cbt.lpcs))
			if cs.hwndParent == 0 && cs.lpszClass > 0xFFFF && u16ptrToString(cs.lpszClass) == "webview" {
				if subclassCB != 0 {
					pSetWindowSubclass.Call(wParam, subclassCB, 1, 0)
				}
				if offscreenSpawn {
					cs.x, cs.y = -32000, -32000
				} else if spawnPosSet {
					cs.x, cs.y = spawnX, spawnY
				}
			}
		}
	}
	r, _, _ := pCallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
}

// Declarar Per-Monitor-V2 ANTES de crear ventanas; si no, WebView2 se renderiza a 96 DPI y
// Windows lo estira -> borroso.
func setDpiAware() {
	if p := user32.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		if r, _, _ := p.Call(^uintptr(0) - 3); r != 0 { // PER_MONITOR_AWARE_V2 (-4)
			return
		}
	}
	if p := shcore.NewProc("SetProcessDpiAwareness"); p.Find() == nil {
		if r, _, _ := p.Call(2); r == 0 {
			return
		}
	}
	user32.NewProc("SetProcessDPIAware").Call()
}

func getDpiForSystem() int {
	if p := user32.NewProc("GetDpiForSystem"); p.Find() == nil {
		if r, _, _ := p.Call(); r != 0 {
			return int(r)
		}
	}
	return 96
}

// roundCorners pide a DWM esquinas redondeadas estilo Win11 (sólo build 22000+). Funciona aunque la
// ventana sea frameless (es un atributo de composición, ajeno al área cliente). En Win10 -> no-op.
func roundCorners(hwnd uintptr) {
	const dwmwaWindowCornerPreference = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
	const dwmwcpRound = 2                  // DWMWCP_ROUND
	pref := int32(dwmwcpRound)
	r, _, _ := pDwmSetWindowAttribute.Call(hwnd, dwmwaWindowCornerPreference,
		uintptr(unsafe.Pointer(&pref)), unsafe.Sizeof(pref))
	dlog("roundCorners hr=", int32(r))
}

// setDarkFrame oscurece el MARCO/borde que DWM dibuja alrededor de la ventana. Sin esto, el borde
// sigue el tema del SISTEMA y sale claro sobre la app oscura. (1) modo oscuro inmersivo + (2) color
// de borde NEGRO explícito (determinista sin importar el tema del sistema).
func setDarkFrame(hwnd uintptr) {
	const (
		dwmwaUseImmersiveDarkMode = 20 // Win11/Win10 2004+
		dwmwaBorderColor          = 34 // DWMWA_BORDER_COLOR (build 22000+)
	)
	on := int32(1)
	pDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on))
	border := uint32(0x00000000) // negro (COLORREF 0x00BBGGRR)
	r, _, _ := pDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&border)), unsafe.Sizeof(border))
	dlog("setDarkFrame borderHr=", int32(r))
}

// ---- ajustar la ventana al tamaño de la imagen --------------------------

// imageConfigSize lee SÓLO el encabezado de la imagen (rápido) para conocer su tamaño en píxeles.
// Sirve para nacer ya con la ventana del tamaño justo (sin un resize visible al abrir).
func imageConfigSize(path string) (int32, int32, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return int32(cfg.Width), int32(cfg.Height), true
}

// primaryWorkArea: rectángulo útil del monitor primario (sin la barra de tareas).
func primaryWorkArea() rect {
	var rc rect
	const spiGetWorkArea = 0x0030
	r, _, _ := pSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rc)), 0)
	if r == 0 || (rc.right == 0 && rc.bottom == 0) {
		return rect{0, 0, sysMetric(smCXSCREEN), sysMetric(smCYSCREEN)}
	}
	return rc
}

// fitWindowSize devuelve el tamaño EXTERIOR de ventana (px físicos) para que el escenario (cliente
// menos la barra de título de 38px) muestre una imagen de iw*ih, recortado al 94% del área útil
// preservando el aspecto. La ventana es frameless -> cliente = ventana entera.
func fitWindowSize(iw, ih int32, wa rect) (int32, int32) {
	tbH := int32(38 * uiScale)
	availW := int32(float64(wa.right-wa.left) * 0.94)
	availH := int32(float64(wa.bottom-wa.top)*0.94) - tbH
	sw, sh := iw, ih
	if availW > 0 && availH > 0 && (sw > availW || sh > availH) {
		rw := float64(availW) / float64(sw)
		rh := float64(availH) / float64(sh)
		k := rw
		if rh < k {
			k = rh
		}
		sw = int32(float64(sw) * k)
		sh = int32(float64(sh) * k)
	}
	if sw < 420 {
		sw = 420
	}
	if sh < 280 {
		sh = 280
	}
	return sw, sh + tbH
}

// fitWindowToImage redimensiona la ventana al tamaño de la imagen y la recentra en su monitor.
// No hace nada si está maximizada o en pantalla completa (ahí se respeta el modo).
func fitWindowToImage(hwnd uintptr, iw, ih int32) {
	if iw <= 0 || ih <= 0 || fullscreen || isMaximized(hwnd) {
		return
	}
	wa := workAreaForWindow(hwnd)
	w, h := fitWindowSize(iw, ih, wa)
	cx := wa.left + (wa.right-wa.left-w)/2
	cy := wa.top + (wa.bottom-wa.top-h)/2
	pSetWindowPos.Call(hwnd, 0, uintptr(cx), uintptr(cy), uintptr(w), uintptr(h), uintptr(swpNOZORDER))
}

func isMaximized(hwnd uintptr) bool {
	var wp windowPlacement
	wp.length = uint32(unsafe.Sizeof(wp))
	pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	return wp.showCmd == swSHOWMAXIMIZED
}

func isMinimized(hwnd uintptr) bool {
	var wp windowPlacement
	wp.length = uint32(unsafe.Sizeof(wp))
	pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	return wp.showCmd == swSHOWMINIMIZED
}

// bringToFront restaura (si está minimizada) y trae la ventana al frente de forma fiable.
func bringToFront(hwnd uintptr) {
	if isMinimized(hwnd) {
		pShowWindow.Call(hwnd, swRESTORE)
	} else {
		pShowWindow.Call(hwnd, swSHOW)
	}
	// toggle topmost: fuerza el z-order al frente aunque no tengamos foreground rights
	pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, uintptr(swpNOMOVE|swpNOSIZE|swpSHOWWINDOW))
	pSetWindowPos.Call(hwnd, hwndNoTopmst, 0, 0, 0, 0, uintptr(swpNOMOVE|swpNOSIZE))
	pSetForegroundWindow.Call(hwnd)
}

// ---- instancia única (daemon caliente) ---------------------------------
// El primer lumen.exe queda corriendo; cada invocación siguiente le manda la ruta por HTTP y
// sale al instante (sin pagar el cold-start de WebView2). El lock guarda "puerto\npid".

func lockPath() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "Lumen", "instance.lock")
}

func writeLock(port string) {
	p := lockPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(port+"\n"+strconv.Itoa(os.Getpid())), 0o644)
}

func removeLock() { os.Remove(lockPath()) }

func readLock() (port string, pid int, ok bool) {
	b, err := os.ReadFile(lockPath())
	if err != nil {
		return "", 0, false
	}
	parts := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(parts) < 2 || parts[0] == "" {
		return "", 0, false
	}
	pid, _ = strconv.Atoi(parts[1])
	return parts[0], pid, true
}

// tryHandoff devuelve true si había una instancia viva que aceptó mostrar la imagen.
func tryHandoff(path string) bool {
	port, pid, ok := readLock()
	if !ok {
		return false
	}
	if pid > 0 {
		pAllowSetForegroundWindow.Call(uintptr(pid)) // dejar que la instancia robe el foco
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/show?path=" + url.QueryEscape(path))
	if err != nil {
		return false // instancia muerta / lock viejo -> arrancamos nosotros
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func subclassProc(hwnd, msg, wParam, lParam, uID, dwRef uintptr) uintptr {
	if msg == wmNCCALCSIZE && wParam != 0 {
		// En pantalla completa, sin inset (tapamos todo el monitor). Maximizado normal, el
		// inset estandar para no comernos un borde. Restaurado, 0 (cliente = ventana entera).
		if !fullscreen && isMaximized(hwnd) {
			p := (*nccalcsizeParams)(unsafe.Pointer(lParam))
			cx := sysMetric(smCXFRAME) + sysMetric(smCXPADDEDBORDER)
			cy := sysMetric(smCYFRAME) + sysMetric(smCXPADDEDBORDER)
			p.rgrc[0].left += cx
			p.rgrc[0].top += cy
			p.rgrc[0].right -= cx
			p.rgrc[0].bottom -= cy
		}
		return 0
	}
	if msg == wmERASEBKGND && darkBrush != 0 {
		var rc rect
		pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		pFillRect.Call(wParam, uintptr(unsafe.Pointer(&rc)), darkBrush)
		return 1
	}
	r, _, _ := pDefSubclassProc.Call(hwnd, msg, wParam, lParam)
	return r
}

func htCode(dir string) uintptr {
	switch dir {
	case "l":
		return htLEFT
	case "r":
		return htRIGHT
	case "t":
		return htTOP
	case "b":
		return htBOTTOM
	case "tl":
		return htTOPLEFT
	case "tr":
		return htTOPRIGHT
	case "bl":
		return htBOTTOMLEFT
	case "br":
		return htBOTTOMRIGHT
	}
	return htCAPTION
}

func enterFullscreen(hwnd uintptr) {
	if fullscreen {
		return
	}
	savedPlc.length = uint32(unsafe.Sizeof(savedPlc))
	pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&savedPlc)))
	savedOK = true
	fullscreen = true
	rc := monitorRect(hwnd)
	pSetWindowPos.Call(hwnd, hwndTopmost,
		uintptr(rc.left), uintptr(rc.top), uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top),
		uintptr(swpFRAMECHANGED|swpSHOWWINDOW))
}

func exitFullscreen(hwnd uintptr) {
	if !fullscreen {
		return
	}
	fullscreen = false
	if savedOK {
		pSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&savedPlc)))
	}
	pSetWindowPos.Call(hwnd, hwndNoTopmst, 0, 0, 0, 0,
		uintptr(swpNOMOVE|swpNOSIZE|swpFRAMECHANGED))
}

func main() {
	runtime.LockOSThread()
	setDpiAware()
	scale := float64(getDpiForSystem()) / 96.0
	uiScale = scale
	loadConfig()

	// Ruta de arranque: argumento de linea de comandos ("Abrir con Lumen" / lumen.exe foto.jpg).
	initialPath := ""
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		if abs, err := filepath.Abs(os.Args[1]); err == nil {
			initialPath = abs
		}
	}

	// Instancia única: si ya hay una ventana de Lumen viva, le mandamos la imagen y salimos
	// al instante (sin pagar el cold-start de WebView2). LUMEN_NEW=1 fuerza ventana nueva.
	if os.Getenv("LUMEN_NEW") == "" && tryHandoff(initialPath) {
		dlog("handoff a instancia existente; saliendo")
		return
	}

	addr := startServer(initialPath)
	pageURL := "http://" + addr + "/"
	dlog("server addr", addr, "initial", initialPath)
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		writeLock(portStr)
		defer removeLock()
	}

	// dark + callback listos ANTES del hook: el CBT hook subclasa la ventana al nacer, para que sea
	// frameless + oscura desde el primer pixel (sin flash de barra nativa / fondo claro).
	darkBrush, _, _ = pCreateSolidBrush.Call(0x000C0908) // COLORREF de #08090C (fondo dark base)
	subclassCB = windows.NewCallback(subclassProc)

	// Posición final (centrada) ANTES de crear: cbtProc la clava en el CREATESTRUCT para que la
	// ventana nazca ahí (sin que go-webview2 la muestre en otro lado y luego salte).
	// Si el modo "ajustar a la imagen" está activo y hay imagen inicial, la ventana NACE ya del
	// tamaño justo (leemos el encabezado con imageConfigSize) -> sin resize visible al abrir.
	winW, winH := uint(1200*scale), uint(820*scale)
	if getFitToImage() && initialPath != "" {
		if iw, ih, ok := imageConfigSize(initialPath); ok {
			fw, fh := fitWindowSize(iw, ih, primaryWorkArea())
			winW, winH = uint(fw), uint(fh)
		}
	}
	spawnX = (sysMetric(smCXSCREEN) - int32(winW)) / 2
	spawnY = (sysMetric(smCYSCREEN) - int32(winH)) / 2
	if spawnX < 0 {
		spawnX = 0
	}
	if spawnY < 0 {
		spawnY = 0
	}
	spawnPosSet = true

	tid, _, _ := pGetCurrentThreadId.Call()
	cbtHook, _, _ := pSetWindowsHookExW.Call(uintptr(whCBT), windows.NewCallback(cbtProc), 0, tid)

	// Cache persistente de WebView2 (perfil + shaders GPU) -> arranques en frío más rápidos.
	dataPath := ""
	if d, err := os.UserCacheDir(); err == nil {
		dataPath = filepath.Join(d, "Lumen", "WebView2")
	}
	// Flags de Chromium: que NO frene el render con la ventana oculta/ocluida (clave para pintar
	// rápido durante el cold-start, mientras todavía no está al frente).
	renderFlags := "--no-first-run --disable-background-networking --disable-component-update " +
		"--disable-backgrounding-occluded-windows --disable-renderer-backgrounding"
	if extra := os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"); extra == "" {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", renderFlags)
	} else {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", extra+" "+renderFlags)
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  "Lumen",
			Width:  winW,
			Height: winH,
			Center: true,
			IconId: 1, // RT_GROUP_ICON embebido por rsrc.syso (lumen.ico)
		},
	})
	if cbtHook != 0 {
		pUnhookWindowsHookEx.Call(cbtHook)
	}
	if w == nil {
		panic("no se pudo crear WebView2")
	}
	defer w.Destroy()

	hwnd := uintptr(w.Window())
	roundCorners(hwnd)          // esquinas redondeadas Win11
	setDarkFrame(hwnd)          // marco/borde DWM en negro (si no, sale claro siguiendo el tema)
	setWebViewDarkBackground(w) // about:blank OSCURO (la ventana ya nació dark+frameless por el hook)
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, uintptr(swpNOMOVE|swpNOSIZE|swpNOZORDER|swpFRAMECHANGED))

	w.SetSize(int(560*scale), int(420*scale), webview2.HintMin)

	// puente JS -> ventana (corre en goroutine del webview -> Dispatch al hilo UI)
	w.Bind("lumenMin", func() {
		w.Dispatch(func() { pShowWindow.Call(hwnd, swMINIMIZE) })
	})
	w.Bind("lumenMaxToggle", func() {
		w.Dispatch(func() {
			if isMaximized(hwnd) {
				pShowWindow.Call(hwnd, swRESTORE)
			} else {
				pShowWindow.Call(hwnd, swMAXIMIZE)
			}
		})
	})
	w.Bind("lumenClose", func() {
		w.Dispatch(func() { pPostMessageW.Call(hwnd, wmCLOSE, 0, 0) })
	})
	w.Bind("lumenDrag", func() {
		w.Dispatch(func() {
			pReleaseCapture.Call()
			pSendMessageW.Call(hwnd, wmNCLBUTTONDOWN, htCAPTION, 0)
		})
	})
	w.Bind("lumenResize", func(dir string) {
		w.Dispatch(func() {
			pReleaseCapture.Call()
			pSendMessageW.Call(hwnd, wmNCLBUTTONDOWN, htCode(dir), 0)
		})
	})
	w.Bind("lumenFullscreen", func(on bool) {
		w.Dispatch(func() {
			if on {
				enterFullscreen(hwnd)
			} else {
				exitFullscreen(hwnd)
			}
		})
	})
	// Dialogo nativo de apertura. Corre en el hilo UI y le pasa la ruta a la pagina via Eval.
	w.Bind("lumenPick", func() {
		w.Dispatch(func() {
			if p := pickImage(hwnd); p != "" {
				if b, err := json.Marshal(p); err == nil {
					w.Eval("window.__lumenOpen(" + string(b) + ")")
				}
			}
		})
	})
	// modo "ajustar ventana a la imagen": persistir el toggle y redimensionar la ventana al vuelo.
	w.Bind("lumenSetFit", func(on bool) { setFitToImage(on) })
	w.Bind("lumenFitTo", func(iw, ih int) {
		w.Dispatch(func() { fitWindowToImage(hwnd, int32(iw), int32(ih)) })
	})

	// mostrar/centra la ventana recien cuando la pagina aviso que pinto (evita flash en blanco)
	var shownOnce sync.Once
	showWin := func() {
		shownOnce.Do(func() {
			var rc rect
			pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			ww, hh := rc.right-rc.left, rc.bottom-rc.top
			cx, cy := (sysMetric(smCXSCREEN)-ww)/2, (sysMetric(smCYSCREEN)-hh)/2
			if cx < 0 {
				cx = 0
			}
			if cy < 0 {
				cy = 0
			}
			after := uintptr(0)
			flags := uintptr(swpNOSIZE | swpNOZORDER | swpSHOWWINDOW)
			if os.Getenv("LUMEN_TOPMOST") != "" { // solo para captura/verificacion
				after = hwndTopmost
				flags = uintptr(swpNOSIZE | swpSHOWWINDOW)
			}
			pSetWindowPos.Call(hwnd, after, uintptr(cx), uintptr(cy), 0, 0, flags)
			pSetForegroundWindow.Call(hwnd)
			// Nudge de tamaño: fuerza WM_SIZE -> go-webview2 reajusta los bounds del
			// controlador y WebView2 re-presenta su swapchain en la ventana ya visible
			// (sin esto el contenido se renderiza pero no se compone en pantalla).
			pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(ww), uintptr(hh+1), uintptr(swpNOMOVE|swpNOZORDER))
			pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(ww), uintptr(hh), uintptr(swpNOMOVE|swpNOZORDER))
		})
	}
	w.Bind("lumenReady", func() { w.Dispatch(showWin) })
	time.AfterFunc(8*time.Second, func() { w.Dispatch(showWin) }) // rescate si la pagina nunca avisa

	initJS := "window.__LUMEN_HOST__=true;"
	initJS += fmt.Sprintf("window.__LUMEN_FIT__=%t;", getFitToImage())
	if debugLog {
		initJS += "window.__LUMEN_DEBUG__=true;"
	}
	// Instancia única: cuando otra invocación manda una imagen, la mostramos en ESTA ventana
	// (sin abrir otra) y la traemos al frente.
	onShow = func(p string) {
		dlog("show", p)
		w.Dispatch(func() {
			if p != "" {
				if b, err := json.Marshal(p); err == nil {
					w.Eval("window.__lumenOpen(" + string(b) + ")")
				}
			}
			bringToFront(hwnd)
		})
	}

	w.Init(initJS)
	dlog("navigating to", pageURL)
	w.Navigate(pageURL)
	dlog("entering run loop")
	w.Run()
}

// ----------------------------------------------------------------------
// Server HTTP local: sirve la UI embebida + la lista de la carpeta + los bytes de cada imagen.
// ----------------------------------------------------------------------

// Formatos que WebView2/Chromium renderiza nativamente: se sirven crudos (rapido, con range).
var webNativeExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".jfif": true, ".jif": true, ".pjpeg": true, ".pjp": true,
	".png": true, ".apng": true,
	".gif": true, ".webp": true, ".avif": true,
	".bmp": true, ".dib": true,
	".ico": true, ".cur": true,
	".svg": true,
}

// Formatos que el navegador NO soporta pero Go SÍ: se decodifican y reencodean a PNG al vuelo.
var decodeExts = map[string]bool{
	".tif": true, ".tiff": true,
}

func isImage(name string) bool {
	e := strings.ToLower(filepath.Ext(name))
	return webNativeExts[e] || decodeExts[e]
}

func startServer(initialPath string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// ruta inicial (argumento de CLI) que la pagina consulta al cargar
	mux.HandleFunc("/api/initial", func(wr http.ResponseWriter, r *http.Request) {
		writeJSON(wr, map[string]string{"path": initialPath})
	})

	// instancia única: otra invocación de lumen.exe nos manda acá la imagen a mostrar.
	mux.HandleFunc("/api/show", func(wr http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p != "" {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		if onShow != nil {
			onShow(p)
		}
		wr.WriteHeader(http.StatusOK)
	})

	// abrir: recibe una ruta (archivo o carpeta), escanea su carpeta y devuelve la lista
	// ordenada de imagenes + el indice de la elegida.
	mux.HandleFunc("/api/open", func(wr http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		list, idx, dir, err := scanFolder(p)
		if err != nil {
			wr.WriteHeader(http.StatusNotFound)
			writeJSON(wr, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(wr, map[string]any{
			"ok": true, "dir": dir, "index": idx, "count": len(list), "images": list,
		})
	})

	// bytes de una imagen (lo que apunta cada <img src>). Nativa -> cruda; no-nativa -> PNG.
	mux.HandleFunc("/file", func(wr http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			wr.WriteHeader(http.StatusForbidden)
			return
		}
		ext := strings.ToLower(filepath.Ext(p))
		if decodeExts[ext] {
			serveDecoded(wr, r, p)
			return
		}
		if !webNativeExts[ext] {
			wr.WriteHeader(http.StatusForbidden)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			wr.WriteHeader(http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			wr.WriteHeader(http.StatusNotFound)
			return
		}
		wr.Header().Set("Cache-Control", "max-age=86400")
		http.ServeContent(wr, r, filepath.Base(p), st.ModTime(), f)
	})

	// canal de logs desde la pagina (window.onerror / pasos de arranque)
	mux.HandleFunc("/log", func(wr http.ResponseWriter, r *http.Request) {
		dlog("JS:", r.URL.Query().Get("m"))
		wr.WriteHeader(http.StatusNoContent)
	})

	var handler http.Handler = mux
	if debugLog {
		handler = http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
			dlog("HTTP", r.Method, r.URL.Path)
			mux.ServeHTTP(wr, r)
		})
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	return ln.Addr().String()
}

func writeJSON(wr http.ResponseWriter, v any) {
	wr.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(wr).Encode(v)
}

// serveDecoded decodifica con Go un formato que el navegador no entiende (TIFF, etc.) y lo
// reencoda a PNG para que el <img> lo muestre.
func serveDecoded(wr http.ResponseWriter, r *http.Request, p string) {
	f, err := os.Open(p)
	if err != nil {
		wr.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		wr.WriteHeader(http.StatusNotFound)
		return
	}
	img, _, err := image.Decode(f)
	if err != nil {
		dlog("decode error", p, err)
		wr.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		wr.WriteHeader(http.StatusInternalServerError)
		return
	}
	wr.Header().Set("Cache-Control", "max-age=3600")
	http.ServeContent(wr, r, "image.png", st.ModTime(), bytes.NewReader(buf.Bytes()))
}

// scanFolder toma un archivo o carpeta y devuelve (imagenes ordenadas, indice del archivo, carpeta).
func scanFolder(p string) ([]string, int, string, error) {
	if p == "" {
		return nil, 0, "", os.ErrInvalid
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, 0, "", err
	}

	dir := p
	target := ""
	if !info.IsDir() {
		dir = filepath.Dir(p)
		target = strings.ToLower(filepath.Base(p))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && isImage(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })

	idx := 0
	list := make([]string, len(names))
	for i, n := range names {
		list[i] = filepath.Join(dir, n)
		if strings.ToLower(n) == target {
			idx = i
		}
	}
	return list, idx, dir, nil
}

// naturalLess ordena "natural": foto2 antes que foto10, sin distinguir mayusculas.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		da, db := ca >= '0' && ca <= '9', cb >= '0' && cb <= '9'
		if da && db {
			si, sj := i, j
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}
