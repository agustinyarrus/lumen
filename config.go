package main

// config.go — preferencias persistentes en %AppData%\Lumen\config.json.
//
// Por qué server-side y no localStorage: el server escucha en un puerto EFÍMERO distinto cada
// arranque (127.0.0.1:0), y localStorage está particionado por origen (incluye el puerto), así que
// cada apertura sería un origen nuevo y se perdería todo.
//
//   fitToImage : al abrir/navegar, ¿ajustar la VENTANA al tamaño de la imagen? (si no, se conserva
//                el tamaño de ventana y la imagen se ajusta adentro).
//   lang       : idioma elegido con el botón de la barra ("es" / "en"); vacío = inglés por defecto.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type appConfig struct {
	FitToImage bool   `json:"fitToImage"`
	Lang       string `json:"lang,omitempty"` // "es", "en" o vacío (automático)
}

var (
	gCfg   appConfig
	gCfgMu sync.Mutex
)

func configPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		if d, err = os.UserCacheDir(); err != nil {
			d = os.TempDir()
		}
	}
	return filepath.Join(d, "Lumen", "config.json")
}

func loadConfig() {
	gCfgMu.Lock()
	defer gCfgMu.Unlock()
	b, err := os.ReadFile(configPath())
	if err != nil {
		return
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) // tolerar BOM si lo editaron a mano
	_ = json.Unmarshal(b, &gCfg)
}

func saveConfigLocked() {
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, err := json.MarshalIndent(gCfg, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, p) // reemplazo atómico
	}
}

func getFitToImage() bool {
	gCfgMu.Lock()
	defer gCfgMu.Unlock()
	return gCfg.FitToImage
}

// langSetting es el idioma guardado ("" = automático: inglés si nada más elige).
func langSetting() string {
	gCfgMu.Lock()
	defer gCfgMu.Unlock()
	return gCfg.Lang
}

// setLangSetting guarda la elección del usuario. Sólo "es" o "en": cualquier otra cosa vuelve a
// automático, así un config.json editado a mano nunca deja la app en un idioma inexistente.
func setLangSetting(code string) {
	if code != "es" && code != "en" {
		code = ""
	}
	gCfgMu.Lock()
	defer gCfgMu.Unlock()
	gCfg.Lang = code
	saveConfigLocked()
}

func setFitToImage(on bool) {
	gCfgMu.Lock()
	defer gCfgMu.Unlock()
	gCfg.FitToImage = on
	saveConfigLocked()
}
