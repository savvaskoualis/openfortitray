; OpenFortiTray Windows installer (Inno Setup 6).
;
; Reproduces the outcomes of scripts/install.ps1 as a double-click wizard:
;   - copies the CI-built openfortitray-windows-amd64.exe to
;     %ProgramFiles%\openfortitray\openfortitray.exe,
;   - installs openconnect via winget when winget is present and openconnect is
;     not already on PATH (not bundled: licensing + size),
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

[Icons]
; Start-menu shortcut -> {autoprograms}\OpenFortiTray.lnk, same as install.ps1.
Name: "{autoprograms}\OpenFortiTray"; Filename: "{app}\openfortitray.exe"; WorkingDir: "{app}"; Comment: "OpenFortiTray - FortiGate SSL-VPN tray client"

[Run]
; 1. openconnect via winget — only when winget is present AND openconnect is not
;    already on PATH. Not bundled (licensing + size). If winget is absent the
;    step is skipped (Check) and the README documents installing openconnect
;    manually; the app still installs and runs, only the tunnel needs openconnect.
Filename: "{cmd}"; \
  Parameters: "/C winget install --accept-package-agreements --accept-source-agreements OpenConnect.OpenConnect"; \
  StatusMsg: "Installing openconnect via winget..."; \
  Flags: runhidden waituntilterminated; \
  Check: ShouldInstallOpenConnect

; 2. Elevated ONLOGON scheduled task. Byte-identical command to install.ps1:
;    schtasks /Create /TN "OpenFortiTray" /SC ONLOGON /RL HIGHEST /TR "<quoted exe>" /F
;    The /TR value is wrapped in literal double quotes so Task Scheduler keeps
;    the path intact under Program Files at launch (see autostart_windows.go).
;    /F recreates it in place, so re-running the installer is idempotent.
Filename: "{sys}\schtasks.exe"; \
  Parameters: "/Create /TN ""OpenFortiTray"" /SC ONLOGON /RL HIGHEST /TR ""\""{app}\openfortitray.exe\"""" /F"; \
  StatusMsg: "Registering the logon task..."; \
  Flags: runhidden waituntilterminated

; 3. Optionally launch the app after install (skipped on silent installs).
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
{ Returns True when `where <exe>` finds the program on PATH. }
function OnPath(Exe: String): Boolean;
var
  ResultCode: Integer;
begin
  Result := Exec(ExpandConstant('{cmd}'),
                 '/C where ' + Exe + ' >nul 2>&1',
                 '', SW_HIDE, ewWaitUntilTerminated, ResultCode)
            and (ResultCode = 0);
end;

{ Install openconnect only if winget exists and openconnect is not already
  present — mirrors install.ps1, which skips winget when openconnect resolves. }
function ShouldInstallOpenConnect(): Boolean;
begin
  Result := OnPath('winget') and (not OnPath('openconnect'));
end;

{ After a non-silent install, if winget was unavailable, point the user at the
  README so they know the tunnel needs openconnect installed separately. }
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
  begin
    if (not WizardSilent) and (not OnPath('openconnect')) and (not OnPath('winget')) then
      MsgBox('openconnect was not found and winget is unavailable to install it.'
             + #13#10#13#10
             + 'OpenFortiTray is installed, but the VPN tunnel needs openconnect. '
             + 'Install it manually (see the README''s Windows section), then set '
             + '"openconnect_path" in %APPDATA%\openfortitray\config.json if it is '
             + 'not on PATH.',
             mbInformation, MB_OK);
  end;
end;
