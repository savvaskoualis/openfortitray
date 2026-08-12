# Installs OpenFortiTray on Windows: the bundled openconnect, the tray binary, and
# an elevated logon task (which is also the app's elevation mechanism — openconnect
# needs admin for the wintun adapter). Idempotent: /F recreates the task in place.
#
# Run from an elevated PowerShell, with openfortitray-windows-amd64.exe next to this
# script (copy it from dist\ after `make release`, or download the release).
#
# Set $env:OPENFORTITRAY_GATEWAY = "vpn.example.com:10443" first, or answer the prompt.
$ErrorActionPreference = "Stop"

$installDir = Join-Path $env:ProgramFiles "openfortitray"
$exeSource  = Join-Path $PSScriptRoot "openfortitray-windows-amd64.exe"
$exeTarget  = Join-Path $installDir "openfortitray.exe"

if (-not (Test-Path $exeSource)) {
    throw "openfortitray-windows-amd64.exe not found next to this script; run 'make release' and copy it here"
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null

# 1. Gateway. There is no built-in default (it is deployment-specific), so it has
#    to be written before the first launch or the tray reports "gateway not set".
#    An existing config.json is left alone: it holds the user's own settings and
#    this script has no merge logic.
$configDir  = Join-Path $env:APPDATA "openfortitray"
$configFile = Join-Path $configDir "config.json"
if (Test-Path $configFile) {
    Write-Host "$configFile already exists; leaving it alone"
} else {
    $gateway = $env:OPENFORTITRAY_GATEWAY
    if (-not $gateway) {
        $gateway = Read-Host "VPN gateway as host:port (e.g. vpn.example.com:10443)"
    }
    # Same shape as the POSIX helper's validate_gateway: a value this accepts is
    # one openconnect will be handed unchanged. The first character must not be a
    # dash — "-form-value:443" is a well-formed host:port to a naive pattern and an
    # openconnect flag to openconnect.
    if ($gateway -notmatch '^[A-Za-z0-9._][A-Za-z0-9._-]*:[0-9]+$') {
        throw "gateway must be host:port, got '$gateway'"
    }
    $parts = $gateway.Split(":")
    New-Item -ItemType Directory -Force -Path $configDir | Out-Null
    @{ gateway = $parts[0]; port = [int]$parts[1] } |
        ConvertTo-Json | Set-Content -Path $configFile -Encoding UTF8
    Write-Host "wrote $configFile (gateway $gateway)"
}

# 2. Bundled openconnect. Windows has no reliable way to obtain openconnect
#    (winget is a dead stub on locked-down Cloud PCs), so the release ships it —
#    openconnect.exe, its full DLL closure, and wintun.dll — in an "openconnect"
#    folder next to this script (CI collects it; the Setup.exe wizard is the
#    turnkey path). Copy that folder next to the exe; the tray resolves
#    <installDir>\openconnect\openconnect.exe at runtime when config still holds
#    the bare "openconnect" default. If the folder is absent (you downloaded only
#    the bare exe), install openconnect manually and set "openconnect_path" in
#    %APPDATA%\openfortitray\config.json to its full path, or use the Setup.exe.
$ocSource = Join-Path $PSScriptRoot "openconnect"
$ocTarget = Join-Path $installDir "openconnect"
if (Test-Path $ocSource) {
    New-Item -ItemType Directory -Force -Path $ocTarget | Out-Null
    Copy-Item (Join-Path $ocSource "*") $ocTarget -Recurse -Force
    Write-Host "copied bundled openconnect from $ocSource"
} elseif (-not (Get-Command openconnect -ErrorAction SilentlyContinue)) {
    Write-Host "note: bundled 'openconnect' folder not found next to this script and openconnect is not on PATH."
    Write-Host "      Use the Setup.exe installer (bundles openconnect), or install openconnect manually and set"
    Write-Host "      'openconnect_path' in $configFile to its full path."
}

# 3. Tray binary.
Copy-Item $exeSource $exeTarget -Force

# 3b. Bundled Mesa llvmpipe software OpenGL. Windows loads an app-directory
#     opengl32.dll before the system one, so copying Mesa's opengl32.dll (plus
#     its llvmpipe backend libgallium_wgl.dll) next to the exe makes the tray
#     render in software on GPU-less boxes (VMs/RDP) that have no GL driver.
#     The release ships both DLLs beside openfortitray-windows-amd64.exe; if you
#     downloaded only the bare exe, download opengl32.dll and libgallium_wgl.dll
#     from the same release and drop them next to this script. Both are needed:
#     opengl32.dll is a thin front-end that loads libgallium_wgl.dll. Skipped
#     (with a note) if absent — the app still runs where a real GL driver exists.
foreach ($dll in @("opengl32.dll", "libgallium_wgl.dll")) {
    $dllSource = Join-Path $PSScriptRoot $dll
    if (Test-Path $dllSource) {
        Copy-Item $dllSource (Join-Path $installDir $dll) -Force
        Write-Host "copied $dll (bundled software OpenGL)"
    } else {
        Write-Host "note: $dll not found next to this script; on a GPU-less machine (VM/RDP) download it from the release and place it here, then re-run"
    }
}

# 4. Start Menu shortcut so the app shows up in Start search (the logon task
#    launches it at login; without this it is nowhere in the Start menu). Written
#    to the per-user Programs folder and overwritten each run so it stays current.
#    IconLocation points at the exe itself: if the exe embeds an icon the shortcut
#    shows it, otherwise Windows falls back to the generic exe icon — the shortcut
#    still launches either way.
$startMenuDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$shortcutPath = Join-Path $startMenuDir "OpenFortiTray.lnk"
New-Item -ItemType Directory -Force -Path $startMenuDir | Out-Null
$wsh = New-Object -ComObject WScript.Shell
$shortcut = $wsh.CreateShortcut($shortcutPath)
$shortcut.TargetPath  = $exeTarget
$shortcut.IconLocation = $exeTarget
$shortcut.WorkingDirectory = $installDir
$shortcut.Description = "OpenFortiTray - FortiGate SSL-VPN tray client"
$shortcut.Save()
Write-Host "wrote Start Menu shortcut $shortcutPath"

# 5. Elevated logon task. /RL HIGHEST runs the app elevated so openconnect
#    inherits admin; the quoted /TR value survives spaces in Program Files.
#    Task name must stay "OpenFortiTray" — internal/autostart toggles this exact
#    task from the tray checkbox.
schtasks /Create /TN "OpenFortiTray" /SC ONLOGON /RL HIGHEST /TR "`"$exeTarget`"" /F | Out-Null

# 6. Start now.
Start-ScheduledTask -TaskName "OpenFortiTray"
Write-Host "OpenFortiTray installed; tray icon should appear. First connect opens a browser SAML login."
Write-Host "Quit FortiClient before connecting."
