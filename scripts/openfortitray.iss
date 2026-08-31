; OpenFortiTray Windows installer (Inno Setup 6).
;
; Reproduces the outcomes of scripts/install.ps1 as a double-click wizard:
;   - copies the CI-built openfortitray-windows-amd64.exe to
;     %ProgramFiles%\openfortitray\openfortitray.exe,
;   - installs the bundled openconnect + its DLL closure + wintun.dll into
;     {app}\openconnect (there is no reliable way to get openconnect onto a
;     locked-down Cloud PC — winget is a dead stub there — so it is shipped, the
;     same pattern used for Mesa; the app resolves this binary at runtime),
;   - creates the elevated ONLOGON scheduled task "OpenFortiTray"
;     (/RL HIGHEST) — the same task internal/autostart toggles from the tray and
;     the same command install.ps1 runs, so the task name stays byte-identical,
;   - adds a Start-menu shortcut,
;   - optionally launches the app.
;
; Requires admin: it writes Program Files and creates an elevated task.
;
; Build (CI, from the repo root):
;   iscc /DMyAppVersion=1.2.3 scripts/openfortitray.iss
; The freshly built exe is expected at ..\dist\openfortitray-windows-amd64.exe
; relative to this script (override with /DMyAppExe=... if it lives elsewhere).
; The bundled openconnect dir is expected at ..\dist\openconnect (override with
; /DMyOcDir=...); CI's "Bundle openconnect + DLL closure + wintun" step fills it.
;
; This installer redistributes third-party binaries: Mesa 3D
; (https://mesa3d.org/) — the llvmpipe software OpenGL driver (opengl32.dll +
; libgallium_wgl.dll), MIT-style — so the tray renders on GPU-less Windows
; (VMs/RDP); and openconnect (LGPL-2.1) with its dependency DLLs and Wintun. See
; THIRD_PARTY_LICENSES in the repository root for full attribution and the LGPL
; written-offer / relink notice.
;
; UNVERIFIED: authored on a non-Windows host and never run through ISCC or on a
; real Windows machine. Review by inspection only.

; AppVersion comes from CI's /DMyAppVersion. Fall back so a bare `iscc` still
; compiles for local smoke checks. AppVersion is left as a plain string (never
; fed to VersionInfoVersion) so pre-release tags like 1.2.3-rc1 do not trip
; Inno's numeric x.x.x.x parser.
#ifndef MyAppVersion
  #define MyAppVersion "0.0.0-dev"
#endif

; Source of the built tray exe. CI places it in dist/ next to this script's
; parent; override with /DMyAppExe if needed.
#ifndef MyAppExe
  #define MyAppExe "..\dist\openfortitray-windows-amd64.exe"
#endif

; Directory holding the bundled Mesa llvmpipe GL DLLs (opengl32.dll +
; libgallium_wgl.dll). CI's "Bundle Mesa software OpenGL" step extracts them
; into dist/ before ISCC runs, so they sit beside the exe. Override with
; /DMyMesaDir if they live elsewhere.
#ifndef MyMesaDir
  #define MyMesaDir "..\dist"
#endif

; Directory holding the Qt6 runtime DLLs + platform plugin that CI's "Bundle
; Qt6 runtime DLLs" step (windeployqt6) drops next to the exe (miqt
; migration; the exe now links Qt6 at runtime, unlike the old static-binary
; fyne build). windeployqt6 places the Qt*.dll files flat in this directory
; and the platform plugin (qwindows.dll) in a platforms\ subdirectory below
; it. Override with /DMyQtDir if it lives elsewhere.
#ifndef MyQtDir
  #define MyQtDir "..\dist"
#endif

; Directory holding the bundled openconnect binary, its full transitive DLL
; closure, and wintun.dll. CI's "Bundle openconnect + DLL closure + wintun" step
; collects them into dist/openconnect/ before ISCC runs. Override with /DMyOcDir.
#ifndef MyOcDir
  #define MyOcDir "..\dist\openconnect"
#endif

; Where the finished OpenFortiTray-<version>-Setup.exe lands. Defaults to the
; same dist/ directory CI uploads from.
#ifndef MyOutputDir
  #define MyOutputDir "..\dist"
#endif

#define MyAppName "OpenFortiTray"
#define MyAppPublisher "savvaskoualis"

[Setup]
; AppId is a fixed GUID: it keys the uninstall entry and upgrade detection, so
; it must never change across releases. Generated once for OpenFortiTray.
AppId={{B7E4D2A1-3C56-4F8B-A9D2-1E5C7B3F0A84}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
; Program Files\openfortitray — lowercase folder to match install.ps1 and the
; README uninstall instructions (%ProgramFiles%\openfortitray).
DefaultDirName={autopf}\openfortitray
DisableProgramGroupPage=yes
; Writes Program Files and creates an elevated (/RL HIGHEST) task, so the whole
; install needs admin.
PrivilegesRequired=admin
; The tray binary is amd64, so install as a 64-bit app: {autopf} resolves to the
; real Program Files (not the WOW64 x86 folder). "x64" is used (not the newer
; "x64compatible") so this compiles on the pinned Inno Setup 6.2.2 in CI.
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
OutputDir={#MyOutputDir}
OutputBaseFilename=OpenFortiTray-{#MyAppVersion}-Setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayName={#MyAppName}
UninstallDisplayIcon={app}\openfortitray.exe

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
; The CI-built tray exe, renamed to openfortitray.exe at the install target
; (matches install.ps1's %ProgramFiles%\openfortitray\openfortitray.exe).
Source: "{#MyAppExe}"; DestDir: "{app}"; DestName: "openfortitray.exe"; Flags: ignoreversion
; Bundled Mesa llvmpipe software OpenGL, installed into {app} beside the exe.
; Windows loads an app-directory opengl32.dll before the system one, so the tray
; renders in software on GPU-less machines (VMs/RDP) that have no GL driver.
; opengl32.dll is a thin WGL front-end that loads its driver from
; libgallium_wgl.dll, so BOTH must ship. Removed automatically on uninstall
; (Inno tracks [Files] it installs). Mesa is MIT-style — see THIRD_PARTY_LICENSES.
Source: "{#MyMesaDir}\opengl32.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyMesaDir}\libgallium_wgl.dll"; DestDir: "{app}"; Flags: ignoreversion
; Bundled Qt6 runtime (miqt migration): windeployqt6 drops the Qt*.dll files
; flat beside the exe and the platform plugin (qwindows.dll) into a
; platforms\ subdirectory. Both are required at runtime now that the exe
; links Qt6 (see cmd/openfortitray/qtapp.go). Tracked by Inno, so uninstall
; removes them.
Source: "{#MyQtDir}\Qt6*.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyQtDir}\platforms\*"; DestDir: "{app}\platforms"; Flags: ignoreversion
; Bundled openconnect.exe + its full transitive DLL closure + wintun.dll,
; installed into {app}\openconnect. The tray resolves this path at runtime
; (resolveOpenconnectPath: <exeDir>\openconnect\openconnect.exe) when the config
; still holds the bare "openconnect" default, so the tunnel works with no
; openconnect on PATH. recursesubdirs is defensive (the dir is currently flat).
; Tracked by Inno, so uninstall removes them. openconnect is LGPL-2.1 — see
; THIRD_PARTY_LICENSES for the written-offer / relink notice.
Source: "{#MyOcDir}\*"; DestDir: "{app}\openconnect"; Flags: recursesubdirs ignoreversion

[Icons]
; Start-menu shortcut -> {autoprograms}\OpenFortiTray.lnk, same as install.ps1.
Name: "{autoprograms}\OpenFortiTray"; Filename: "{app}\openfortitray.exe"; WorkingDir: "{app}"; Comment: "OpenFortiTray - FortiGate SSL-VPN tray client"

[Run]
; Elevated ONLOGON scheduled task. Byte-identical command to install.ps1:
;    schtasks /Create /TN "OpenFortiTray" /SC ONLOGON /RL HIGHEST /TR "<quoted exe>" /F
;    The /TR value is wrapped in literal double quotes so Task Scheduler keeps
;    the path intact under Program Files at launch (see autostart_windows.go).
;    /F recreates it in place, so re-running the installer is idempotent.
Filename: "{sys}\schtasks.exe"; \
  Parameters: "/Create /TN ""OpenFortiTray"" /SC ONLOGON /RL HIGHEST /TR ""\""{app}\openfortitray.exe\"""" /F"; \
  StatusMsg: "Registering the logon task..."; \
  Flags: runhidden waituntilterminated

; Optionally launch the app after install (skipped on silent installs).
;    shellexec is REQUIRED: the exe ships a requireAdministrator manifest, and a
;    postinstall Run entry launches as the original (non-elevated) user via
;    CreateProcess, which cannot start an elevation-requiring exe (fails with
;    "code 740, requires elevation"). ShellExecuteEx honours the manifest and
;    raises the UAC prompt, matching how the Start-menu shortcut launches it.
Filename: "{app}\openfortitray.exe"; \
  Description: "Launch OpenFortiTray"; \
  WorkingDir: "{app}"; \
  Flags: postinstall nowait skipifsilent shellexec

[UninstallRun]
; Remove the logon task first, before the exe is deleted. RunOnceId guards
; against a double run.
Filename: "{sys}\schtasks.exe"; \
  Parameters: "/Delete /TN ""OpenFortiTray"" /F"; \
  Flags: runhidden waituntilterminated; \
  RunOnceId: "DelOpenFortiTrayTask"
