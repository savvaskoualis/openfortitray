# Installs Postern on Windows: openconnect, the tray binary, and an elevated
# logon task (which is also the app's elevation mechanism — openconnect needs
# admin for the wintun adapter). Idempotent: /F recreates the task in place.
#
# Run from an elevated PowerShell, with postern-windows-amd64.exe next to this
# script (copy it from dist\ after `make release`, or download the release).
#
# Set $env:POSTERN_GATEWAY = "vpn.example.com:10443" first, or answer the prompt.
$ErrorActionPreference = "Stop"

$installDir = Join-Path $env:ProgramFiles "postern"
$exeSource  = Join-Path $PSScriptRoot "postern-windows-amd64.exe"
$exeTarget  = Join-Path $installDir "postern.exe"

if (-not (Test-Path $exeSource)) {
    throw "postern-windows-amd64.exe not found next to this script; run 'make release' and copy it here"
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null

# 1. Gateway. There is no built-in default (it is deployment-specific), so it has
#    to be written before the first launch or the tray reports "gateway not set".
#    An existing config.json is left alone: it holds the user's own settings and
#    this script has no merge logic.
$configDir  = Join-Path $env:APPDATA "postern"
$configFile = Join-Path $configDir "config.json"
if (Test-Path $configFile) {
    Write-Host "$configFile already exists; leaving it alone"
} else {
    $gateway = $env:POSTERN_GATEWAY
    if (-not $gateway) {
        $gateway = Read-Host "VPN gateway as host:port (e.g. vpn.example.com:10443)"
    }
    # Same shape as the POSIX helper's validate_gateway: a value this accepts is
    # one openconnect will be handed unchanged.
    if ($gateway -notmatch '^[A-Za-z0-9._-]+:[0-9]+$') {
        throw "gateway must be host:port, got '$gateway'"
    }
    $parts = $gateway.Split(":")
    New-Item -ItemType Directory -Force -Path $configDir | Out-Null
    @{ gateway = $parts[0]; port = [int]$parts[1] } |
        ConvertTo-Json | Set-Content -Path $configFile -Encoding UTF8
    Write-Host "wrote $configFile (gateway $gateway)"
}

# 2. openconnect (verify the package id with `winget search openconnect` if
#    this fails; a manual install works too — then set "openconnect_path" in
#    %APPDATA%\postern\config.json to its full path).
if (-not (Get-Command openconnect -ErrorAction SilentlyContinue)) {
    winget install --accept-package-agreements --accept-source-agreements OpenConnect.OpenConnect
}

# 3. Tray binary.
Copy-Item $exeSource $exeTarget -Force

# 4. Elevated logon task. /RL HIGHEST runs the app elevated so openconnect
#    inherits admin; the quoted /TR value survives spaces in Program Files.
#    Task name must stay "Postern" — internal/autostart toggles this exact
#    task from the tray checkbox.
schtasks /Create /TN "Postern" /SC ONLOGON /RL HIGHEST /TR "`"$exeTarget`"" /F | Out-Null

# 5. Start now.
Start-ScheduledTask -TaskName "Postern"
Write-Host "Postern installed; tray icon should appear. First connect opens a browser SAML login."
Write-Host "Quit FortiClient before connecting."
