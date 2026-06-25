# build.ps1 — compila Lumen como .exe release (sin ventana de consola, con icono embebido).
# Uso:  .\build.ps1            -> genera lumen.exe
#       .\build.ps1 -Debug     -> genera lumen-debug.exe (con consola + logs LUMEN_DEBUG)

param([switch]$Debug)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# Recurso de icono: regenerar rsrc.syso desde lumen.ico si falta.
if ((Test-Path lumen.ico) -and -not (Test-Path rsrc.syso)) {
  Write-Host "Generando rsrc.syso desde lumen.ico..."
  go run github.com/akavel/rsrc@latest -ico lumen.ico -o rsrc.syso
}

if ($Debug) {
  go build -o lumen-debug.exe .
  Write-Host "OK -> $(Resolve-Path lumen-debug.exe)   (corré con LUMEN_DEBUG=1 para logs)"
} else {
  go build -ldflags="-H windowsgui -s -w" -o lumen.exe .
  Write-Host "OK -> $(Resolve-Path lumen.exe)"
}
