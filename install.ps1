# install.ps1 — instala/desinstala Lumen en Program Files con acceso directo en el menú de inicio.
# Requiere administrador (escribe en Program Files y HKLM). Idempotente.
#
#   .\install.ps1              instala (o reinstala/actualiza)
#   .\install.ps1 -Uninstall   desinstala
#
# Es la base del futuro instalador: copia el .exe, crea el acceso directo, registra la entrada de
# "Agregar o quitar programas" y deja a Lumen disponible en "Abrir con" (sin pisar defaults).

param(
  [string]$InstallDir = "$env:ProgramFiles\Lumen",
  [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
try { Start-Transcript -Path "$env:TEMP\lumen_install.log" -Force | Out-Null } catch {}

$AppName  = 'Lumen'
$Version  = '1.0.0'
$Publisher = 'Agustin Yarrus'
$exeName  = 'lumen.exe'
$src      = $PSScriptRoot

# extensiones que Lumen abre (para "Abrir con")
$exts = @('jpg','jpeg','jpe','jfif','jif','pjpeg','pjp','png','apng','gif','webp','avif','bmp','dib','ico','cur','svg','tif','tiff')

$startMenu = Join-Path "$env:ProgramData\Microsoft\Windows\Start Menu\Programs" "$AppName.lnk"
$uninstKey = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\$AppName"
$appsKey   = "HKLM:\SOFTWARE\Classes\Applications\$exeName"

# --- exigir admin -------------------------------------------------------
$pr = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $pr.IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)) {
  Write-Error "Hay que ejecutar como administrador."
  return
}

# detener cualquier instancia en ejecucion (no se puede sobrescribir un .exe abierto)
Get-Process lumen -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

if ($Uninstall) {
  Remove-Item $startMenu -Force -ErrorAction SilentlyContinue
  Remove-Item $uninstKey -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item $appsKey   -Recurse -Force -ErrorAction SilentlyContinue
  if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue }
  Write-Host "Lumen desinstalado."
  try { Stop-Transcript | Out-Null } catch {}
  return
}

# --- copiar archivos ----------------------------------------------------
New-Item -ItemType Directory -Force $InstallDir | Out-Null
Copy-Item (Join-Path $src $exeName) (Join-Path $InstallDir $exeName) -Force
foreach ($f in 'lumen.ico','README.md','install.ps1') {
  if (Test-Path (Join-Path $src $f)) { Copy-Item (Join-Path $src $f) (Join-Path $InstallDir $f) -Force }
}
$exe = Join-Path $InstallDir $exeName

# --- acceso directo en el menu de inicio (todos los usuarios) -----------
$wsh = New-Object -ComObject WScript.Shell
$lnk = $wsh.CreateShortcut($startMenu)
$lnk.TargetPath       = $exe
$lnk.WorkingDirectory = $InstallDir
$lnk.IconLocation     = "$exe,0"
$lnk.Description       = 'Visor de imagenes minimalista'
$lnk.Save()

# --- entrada en Agregar o quitar programas ------------------------------
New-Item -Path $uninstKey -Force | Out-Null
Set-ItemProperty $uninstKey DisplayName     $AppName
Set-ItemProperty $uninstKey DisplayIcon     "$exe,0"
Set-ItemProperty $uninstKey DisplayVersion  $Version
Set-ItemProperty $uninstKey Publisher       $Publisher
Set-ItemProperty $uninstKey InstallLocation $InstallDir
Set-ItemProperty $uninstKey UninstallString "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$InstallDir\install.ps1`" -Uninstall"
Set-ItemProperty $uninstKey NoModify 1 -Type DWord
Set-ItemProperty $uninstKey NoRepair 1 -Type DWord
try {
  $sz = [math]::Round((Get-Item $exe).Length / 1KB)
  Set-ItemProperty $uninstKey EstimatedSize $sz -Type DWord
} catch {}

# --- registrar la app para "Abrir con" (no pisa el default actual) ------
New-Item -Path "$appsKey\shell\open\command" -Force | Out-Null
Set-ItemProperty $appsKey '(default)' $AppName
Set-ItemProperty $appsKey 'FriendlyAppName' $AppName
New-Item -Path "$appsKey\DefaultIcon" -Force | Out-Null
Set-ItemProperty "$appsKey\DefaultIcon" '(default)' "$exe,0"
Set-ItemProperty "$appsKey\shell\open\command" '(default)' "`"$exe`" `"%1`""
New-Item -Path "$appsKey\SupportedTypes" -Force | Out-Null
foreach ($e in $exts) { Set-ItemProperty "$appsKey\SupportedTypes" ".$e" '' }

Write-Host "Lumen $Version instalado en $InstallDir"
Write-Host "Acceso directo: $startMenu"
try { Stop-Transcript | Out-Null } catch {}
