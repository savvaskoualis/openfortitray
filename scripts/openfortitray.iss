; OpenFortiTray Windows installer (Inno Setup 6).
;
; Reproduces the outcomes of scripts/install.ps1 as a double-click wizard:
;   - copies the CI-built openfortitray-windows-amd64.exe to
;     %ProgramFiles%\openfortitray\openfortitray.exe,
;   - installs the bundled openconnect + its DLL closure + wintun.dll into
;     {app}\openconnect (there is no reliable way to get openconnect onto a
;     locked-down Cloud PC — winget is a dead stub there — so it is shipped;
;     the app resolves this binary at runtime),
;   - installs the Microsoft Edge WebView2 Runtime if it is not already
;     present (most Windows 10/11 machines already have it),
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
; This installer redistributes third-party binaries: openconnect (LGPL-2.1)
; with its dependency DLLs and Wintun. See THIRD_PARTY_LICENSES in the
; repository root for full attribution and the LGPL written-offer / relink
; notice. It also stages (but does not permanently install) the Microsoft
; Edge WebView2 Runtime bootstrapper — Wails' UI renders inside WebView2,
; which ships by default on Windows 11 and most updated Windows 10 machines,
; so this only runs the bootstrapper when the runtime is not already present.
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

; Directory holding the bundled openconnect binary, its full transitive DLL
; closure, and wintun.dll. CI's "Bundle openconnect + DLL closure + wintun" step
; collects them into dist/openconnect/ before ISCC runs. Override with /DMyOcDir.
#ifndef MyOcDir
  #define MyOcDir "..\dist\openconnect"
#endif

; The Microsoft Edge WebView2 Runtime "Evergreen" bootstrapper: a small
; (~2 MB) stub that, when run, downloads and installs the current WebView2
; Runtime from Microsoft's servers if it is not already present. Wails'
; window renders inside WebView2, an OS-provided component (it ships by
; default on Windows 11 and most updated Windows 10 machines via Edge), so
; this only covers the gap on machines without it. CI's "Download WebView2
; Bootstrapper" step fetches it fresh from Microsoft's documented evergreen
; URL into dist/ before ISCC runs (not sha256-pinned like openconnect: this
; stub is *meant* to be Microsoft's latest at any given time — pinning it
; would defeat its purpose). Override with /DMyWebView2Bootstrapper.
#ifndef MyWebView2Bootstrapper
  #define MyWebView2Bootstrapper "..\dist\MicrosoftEdgeWebview2Setup.exe"
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
; The WebView2 Evergreen bootstrapper, staged into {tmp} for the [Run] step
; below rather than permanently installed into {app} — it is a one-shot
; installer, not a runtime component the app itself loads. deleteafterinstall
; (not dontcopy) is what actually extracts the file into DestDir during the
; normal install sequence — dontcopy embeds the file in the installer but
; never extracts it without an explicit ExtractTemporaryFile call in [Code],
; which this script has none of, so the [Run] step below could never
; actually find the file. deleteafterinstall removes it again once install
; finishes, matching dontcopy's "don't leave it behind" intent.
Source: "{#MyWebView2Bootstrapper}"; DestDir: "{tmp}"; Flags: deleteafterinstall
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
; Install the WebView2 Runtime first, if it is not already present (Check:
; below), so the app's window has something to render into on first launch.
; /silent /install runs the bootstrapper's own silent, non-interactive mode
; (Microsoft's documented flags for the Evergreen Bootstrapper).
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; \
  Parameters: "/silent /install"; \
  StatusMsg: "Installing Microsoft Edge WebView2 Runtime..."; \
  Flags: waituntilterminated; \
  Check: not IsWebView2Installed

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

[Code]
// IsWebView2Installed checks WebView2's well-known runtime-detection registry
// key (the "Clients" GUID Microsoft documents for exactly this purpose) so
// the [Run] bootstrapper above is skipped on the many machines that already
// have WebView2 (bundled with Windows 11, or installed alongside Edge on
// Windows 10). The app is x64-only (ArchitecturesInstallIn64BitMode=x64), so
// Inno already reads the native 64-bit registry view here — no HKLM64/WOW6432
// handling needed.
function IsWebView2Installed: Boolean;
var
  Version: string;
begin
  Result := RegQueryStringValue(HKLM,
    'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
    'pv', Version) and (Version <> '');
end;
