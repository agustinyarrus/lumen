; Lumen — instalador (Inno Setup 6). Compilar: ISCC lumen.iss  ->  dist\Lumen-Setup-x.y.z.exe
#define AppName    "Lumen"
#define AppVer     "1.0.0"
#define AppExe     "lumen.exe"
#define AppPub     "Agustin Yarrus"
#define AppUrl     "https://github.com/agustinyarrus/lumen"
#define Exts       ".jpg;.jpeg;.jpe;.jfif;.png;.apng;.gif;.webp;.avif;.bmp;.dib;.ico;.cur;.svg;.tif;.tiff"

[Setup]
AppId={{3B9D2F61-5E8A-4C7B-9A03-1F6E8B2D5C40}
AppName={#AppName}
AppVersion={#AppVer}
AppPublisher={#AppPub}
AppPublisherURL={#AppUrl}
AppSupportURL={#AppUrl}
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
UninstallDisplayIcon={app}\{#AppExe}
LicenseFile=LICENSE
OutputDir=dist
OutputBaseFilename={#AppName}-Setup-{#AppVer}
SetupIconFile=lumen.ico
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
MinVersion=10.0
AppReadmeFile={app}\README.md

[Languages]
Name: "es"; MessagesFile: "compiler:Languages\Spanish.isl"
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked
Name: "openwith";    Description: "Registrar {#AppName} en el menu ""Abrir con"""; GroupDescription: "Integracion con Windows:"

[Files]
Source: "{#AppExe}";  DestDir: "{app}"; Flags: ignoreversion
Source: "lumen.ico";  DestDir: "{app}"; Flags: ignoreversion
Source: "README.md";  DestDir: "{app}"; Flags: ignoreversion isreadme
Source: "LICENSE";    DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}";              Filename: "{app}\{#AppExe}"
Name: "{group}\Desinstalar {#AppName}";  Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}";        Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
Root: HKA; Subkey: "Software\Classes\Applications\{#AppExe}"; ValueType: string; ValueName: "FriendlyAppName"; ValueData: "{#AppName}"; Flags: uninsdeletekey; Tasks: openwith
Root: HKA; Subkey: "Software\Classes\Applications\{#AppExe}\DefaultIcon"; ValueType: string; ValueData: "{app}\{#AppExe},0"; Tasks: openwith
Root: HKA; Subkey: "Software\Classes\Applications\{#AppExe}\shell\open\command"; ValueType: string; ValueData: """{app}\{#AppExe}"" ""%1"""; Tasks: openwith

[Run]
Filename: "{app}\{#AppExe}"; Description: "Abrir {#AppName} ahora"; Flags: nowait postinstall skipifsilent

[Code]
procedure AddSupportedTypes(exeName, csv: String);
var rk: Integer; key, ext: String; p: Integer;
begin
  if IsAdminInstallMode then rk := HKLM else rk := HKCU;
  key := 'Software\Classes\Applications\' + exeName + '\SupportedTypes';
  csv := csv + ';';
  repeat
    p := Pos(';', csv);
    ext := Trim(Copy(csv, 1, p-1));
    Delete(csv, 1, p);
    if ext <> '' then RegWriteStringValue(rk, key, ext, '');
  until Length(csv) = 0;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and WizardIsTaskSelected('openwith') then
    AddSupportedTypes('{#AppExe}', '{#Exts}');
end;
