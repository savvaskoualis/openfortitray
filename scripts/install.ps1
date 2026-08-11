# Installs hyp-vpn on Windows: openconnect, the tray binary, and an elevated
# logon task (which is also the app's elevation mechanism — openconnect needs
# admin for the wintun adapter). Idempotent: /F recreates the task in place.
#
# Run from an elevated PowerShell, with hyp-vpn-windows-amd64.exe next to this
# script (copy it from dist\ after `make release`, or download the release).
$ErrorActionPreference = "Stop"

$installDir = Join-Path $env:ProgramFiles "hyp-vpn"
$exeSource  = Join-Path $PSScriptRoot "hyp-vpn-windows-amd64.exe"
$exeTarget  = Join-Path $installDir "hyp-vpn.exe"

if (-not (Test-Path $exeSource)) {
    throw "hyp-vpn-windows-amd64.exe not found next to this script; run 'make release' and copy it here"
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null

# 1. openconnect (verify the package id with `winget search openconnect` if
#    this fails; a manual install works too — then set "openconnect_path" in
#    %APPDATA%\hyp-vpn\config.json to its full path).
if (-not (Get-Command openconnect -ErrorAction SilentlyContinue)) {
    winget install --accept-package-agreements --accept-source-agreements OpenConnect.OpenConnect
}

# 2. Tray binary.
Copy-Item $exeSource $exeTarget -Force

# 3. Elevated logon task. /RL HIGHEST runs the app elevated so openconnect
#    inherits admin; the quoted /TR value survives spaces in Program Files.
#    Task name must stay "HyperioVPN" — internal/autostart toggles this exact
#    task from the tray checkbox.
schtasks /Create /TN "HyperioVPN" /SC ONLOGON /RL HIGHEST /TR "`"$exeTarget`"" /F | Out-Null

# 4. Start now.
Start-ScheduledTask -TaskName "HyperioVPN"
Write-Host "hyp-vpn installed; tray icon should appear. First connect opens a browser SAML login."
Write-Host "Quit FortiClient before connecting."
