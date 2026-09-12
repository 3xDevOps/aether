[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Cli,
    [switch]$WithoutNode
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if ($env:OS -ne 'Windows_NT') { throw 'This smoke scenario requires Windows.' }
$node = (Get-Command node.exe -CommandType Application).Source
$installer = Join-Path $PSScriptRoot 'install.ps1'
$root = Join-Path ([IO.Path]::GetTempPath()) ('Aether smoke & ' + [guid]::NewGuid().ToString('N'))
$variables = @('LOCALAPPDATA', 'APPDATA', 'USERPROFILE', 'HOME', 'PATH', 'AETHER_BIN',
    'AETHER_BASE_URL', 'AETHER_REPO', 'AETHER_VERSION', 'AETHER_ROLE', 'AETHER_BIN_DIR',
    'AETHER_CONFIG_DIR', 'AETHER_NO_UPDATE_CHECK')
$saved = @{}
foreach ($name in $variables) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$registry = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
$hadUserPath = $registry.GetValueNames() -contains 'Path'
$userPath = $registry.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$userPathKind = if ($hadUserPath) { $registry.GetValueKind('Path') } else { $null }
$server = $null
$app = $null

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $mirror = Join-Path $root 'release'
    New-Item -ItemType Directory -Path $mirror | Out-Null
    $asset = Join-Path $mirror 'aether-windows-amd64.exe'
    Copy-Item -LiteralPath $Cli -Destination $asset
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $asset).Hash
    [IO.File]::WriteAllText((Join-Path $mirror 'checksums.txt'), "$hash  aether-windows-amd64.exe`n")

    $serverScript = Join-Path $root 'release-server.cjs'
    @'
const http = require('node:http')
const fs = require('node:fs')
const path = require('node:path')
const root = process.argv[2]
const files = new Set(['aether-windows-amd64.exe', 'checksums.txt'])
const server = http.createServer((req, res) => {
  const name = req.url.replace('/v0.4.0-alpha.6/', '')
  if (!files.has(name)) { res.writeHead(404); res.end(); return }
  const file = path.join(root, name)
  res.writeHead(200, { 'Content-Length': fs.statSync(file).size })
  fs.createReadStream(file).pipe(res)
})
server.listen(0, '127.0.0.1', () => console.log(server.address().port))
'@ | Set-Content -LiteralPath $serverScript -Encoding UTF8
    $serverLog = Join-Path $root 'release-server.log'
    $server = Start-Process -FilePath $node -ArgumentList @(('"' + $serverScript + '"'), ('"' + $mirror + '"')) -PassThru -RedirectStandardOutput $serverLog -RedirectStandardError (Join-Path $root 'release-server.err')
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($server.HasExited) { throw 'The release fixture server exited.' }
        $port = Get-Content -LiteralPath $serverLog -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($port) { break }
        if ([DateTime]::UtcNow -gt $deadline) { throw 'The release fixture server did not start.' }
        Start-Sleep -Milliseconds 100
    } while ($true)

    foreach ($name in @('AETHER_BIN', 'AETHER_VERSION', 'AETHER_ROLE', 'AETHER_BIN_DIR')) {
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    $env:LOCALAPPDATA = Join-Path $root 'Local'
    $env:APPDATA = Join-Path $root 'Roaming'
    $env:USERPROFILE = Join-Path $root 'Home'
    $env:HOME = $env:USERPROFILE
    $env:AETHER_CONFIG_DIR = Join-Path $root 'config'
    $env:AETHER_NO_UPDATE_CHECK = '1'
    $env:AETHER_BASE_URL = "http://127.0.0.1:$port"
    foreach ($dir in @($env:LOCALAPPDATA, $env:APPDATA, $env:USERPROFILE)) {
        New-Item -ItemType Directory -Path $dir | Out-Null
    }
    if ($WithoutNode) {
        $env:PATH = (($env:PATH -split ';') | Where-Object {
            $_ -and -not (Test-Path -LiteralPath (Join-Path $_ 'node.exe'))
        }) -join ';'
        if (Get-Command node.exe -ErrorAction SilentlyContinue) { throw 'Node is still on PATH.' }
    }
    $cliDir = Join-Path $env:LOCALAPPDATA 'Programs\Aether'
    New-Item -ItemType Directory -Path $cliDir -Force | Out-Null
    $sentinel = Join-Path $cliDir 'unrelated.txt'
    'preserve this file' | Set-Content -LiteralPath $sentinel
    $installed = Join-Path $cliDir 'aether.exe'
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $shortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Aether.lnk'

    foreach ($round in 1..2) {
        & $installer -Version 'v0.4.0-alpha.6'
        if (-not $?) { throw "Automatic install $round failed." }
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash -ne $hash) {
            throw 'Desktop installation changed the CLI.'
        }
        if ((Get-Content -LiteralPath $sentinel) -ne 'preserve this file') { throw 'An unrelated file changed.' }
        if (-not (Test-Path -LiteralPath $desktop)) { throw 'The desktop executable was not installed.' }
        & $installed version
        if ($LASTEXITCODE -ne 0) { throw 'The installed CLI no longer runs.' }
        $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
        if ($link.TargetPath -ne $desktop) { throw "Start Menu target is $($link.TargetPath), not $desktop." }
        if ($link.WorkingDirectory -ne (Split-Path $desktop)) { throw 'The shortcut has the wrong working directory.' }
        Write-Host "Automatic install $round preserved the CLI and registered the desktop."
    }
    if ($WithoutNode -and -not (Get-ChildItem -LiteralPath (Join-Path $env:LOCALAPPDATA 'aether\node') -Filter node.exe -Recurse)) {
        throw 'The build did not provision its private Node runtime.'
    }

    # Model Explorer retaining the PATH from before the installer ran.
    $env:PATH = $saved['PATH']
    [Environment]::SetEnvironmentVariable('AETHER_BIN', $null, 'Process')
    $profile = Join-Path $root 'Electron'
    $app = Start-Process -FilePath $desktop -ArgumentList @('--remote-debugging-port=0', ('--user-data-dir="' + $profile + '"')) -PassThru -RedirectStandardOutput (Join-Path $root 'desktop.log') -RedirectStandardError (Join-Path $root 'desktop.err')
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        $debugLog = Get-Content -LiteralPath (Join-Path $root 'desktop.err') -Raw -ErrorAction SilentlyContinue
        if ($app.HasExited) { throw "Desktop exited: $debugLog" }
        if ($debugLog -match 'DevTools listening on (ws://127\.0\.0\.1:\d+/devtools/browser/[^\s]+)') {
            $endpoint = $Matches[1]
            break
        }
        if ([DateTime]::UtcNow -gt $deadline) { throw "Desktop debugging endpoint did not start: $debugLog" }
        Start-Sleep -Milliseconds 200
    } while ($true)
    $verifyScript = Join-Path $root 'verify-desktop.cjs'
    @'
const { chromium } = require(process.argv[2])
;(async () => {
  const browser = await chromium.connectOverCDP(process.argv[3])
  try {
    const context = browser.contexts()[0]
    const page = context.pages()[0] || await context.waitForEvent('page')
    await page.waitForURL(/^http:\/\/127\.0\.0\.1:\d+\//, { timeout: 45000 })
    await page.getByRole('heading', { name: 'Onboarding', exact: true }).waitFor({ timeout: 45000 })
    await page.screenshot({ path: process.argv[4] })
    console.log('The installed desktop rendered its first-run onboarding screen.')
  } finally {
    await browser.close()
  }
})().catch(error => { console.error(error); process.exitCode = 1 })
'@ | Set-Content -LiteralPath $verifyScript -Encoding UTF8
    $playwright = Join-Path $PSScriptRoot '..\web\node_modules\playwright'
    & $node $verifyScript $playwright $endpoint (Join-Path $env:RUNNER_TEMP 'windows-desktop.png')
    if ($LASTEXITCODE -ne 0) { throw 'The installed desktop did not render onboarding.' }
    Write-Host 'The desktop found the CLI without an updated PATH and loaded its dashboard.'

    $preferences = Get-MpPreference
    if ((Get-MpComputerStatus).RealTimeProtectionEnabled -ne $true -or $preferences.MAPSReporting -ne 2) {
        throw 'Defender realtime/cloud protection is not enabled; this would not verify installation safety.'
    }
    & "$env:ProgramFiles\Windows Defender\MpCmdRun.exe" -Scan -ScanType 3 -File $root
    if ($LASTEXITCODE -ne 0) { throw "Defender installation scan failed (exit $LASTEXITCODE)." }
    Write-Host 'Defender scan of the installer downloads, CLI, Node, and desktop is clean.'
} finally {
    if ($app -and -not $app.HasExited) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $app.Id /T /F | Out-Null
    }
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force }
    foreach ($name in $variables) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    if ($hadUserPath) { $registry.SetValue('Path', $userPath, $userPathKind) } else { $registry.DeleteValue('Path', $false) }
    $registry.Dispose()
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
