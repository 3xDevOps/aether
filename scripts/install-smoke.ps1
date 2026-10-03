[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Cli,
    [switch]$WithoutNode
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if ($env:OS -ne 'Windows_NT') { throw 'This smoke scenario requires Windows.' }
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This smoke changes user shell registrations and requires a disposable GitHub-hosted runner.'
}
$initialPreferences = Get-MpPreference
if ((Get-MpComputerStatus).RealTimeProtectionEnabled -ne $true -or $initialPreferences.MAPSReporting -ne 2) {
    throw 'Enable Defender realtime and cloud protection before running this smoke scenario.'
}
if ($initialPreferences.SubmitSamplesConsent -ne 1) {
    throw 'Enable Defender automatic safe sample submission before running this smoke scenario.'
}
$scanningFlags = @('DisableRealtimeMonitoring', 'DisableIOAVProtection', 'DisableBehaviorMonitoring',
    'DisableScriptScanning', 'DisableArchiveScanning', 'DisableBlockAtFirstSeen')
foreach ($flag in $scanningFlags) {
    if ($initialPreferences.$flag) { throw "Defender $flag is enabled; scanning would be incomplete." }
}
if ($initialPreferences.ExclusionPath -or $initialPreferences.ExclusionProcess -or $initialPreferences.ExclusionExtension) {
    throw 'Run this smoke scenario in a disposable runner without Defender exclusions.'
}
$knownDetections = @(Get-MpThreatDetection | ForEach-Object { $_.DetectionID })
$node = (Get-Command node.exe -CommandType Application | Select-Object -First 1).Source
$installer = Join-Path $PSScriptRoot 'install.ps1'
$root = Join-Path ([IO.Path]::GetTempPath()) ('Aether smoke & ' + [guid]::NewGuid().ToString('N'))
$programs = [Environment]::GetFolderPath([Environment+SpecialFolder]::Programs, [Environment+SpecialFolderOption]::DoNotVerify)
if ([string]::IsNullOrWhiteSpace($programs)) { throw 'The current-user Programs Known Folder could not be resolved.' }
$shortcut = Join-Path $programs 'Aether.lnk'
$shortcutBackup = Join-Path $root 'Aether.lnk.backup'
$shortcutCaptured = $false
$shortcutRestored = $true
$hadShortcut = $false
$variables = @('LOCALAPPDATA', 'PATH', 'AETHER_BIN',
    'AETHER_BASE_URL', 'AETHER_REPO', 'AETHER_VERSION', 'AETHER_ROLE', 'AETHER_BIN_DIR',
    'AETHER_CONFIG_DIR', 'AETHER_NO_UPDATE_CHECK', 'npm_config_cache',
    'ELECTRON_CACHE', 'ELECTRON_BUILDER_CACHE')
$saved = @{}
foreach ($name in $variables) { $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$registry = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
$hadUserPath = $registry.GetValueNames() -contains 'Path'
$userPath = $registry.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$userPathKind = if ($hadUserPath) { $registry.GetValueKind('Path') } else { $null }
$server = $null
$app = $null
$catalogueApp = $null
$installed = $null
$desktop = $null
$protocolKey = 'HKCU:\Software\Classes\aether'
$protocolCaptured = $false
$protocolBackup = Join-Path $root 'protocol.reg'
$reg = Join-Path $env:SystemRoot 'System32\reg.exe'

function Wait-DesktopSidecar([Diagnostics.Process]$Process) {
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        $Process.Refresh()
        if ($Process.HasExited) { throw "The desktop exited before opening its main window (code $($Process.ExitCode))." }
        if ($Process.MainWindowHandle -ne [IntPtr]::Zero) {
            $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $($Process.Id) AND Name = 'aether.exe'" |
                Where-Object { $_.ExecutablePath -ieq $installed })
            if ($children.Count -eq 1) {
                return Get-Process -Id $children[0].ProcessId -ErrorAction Stop
            }
        }
        if ([DateTime]::UtcNow -ge $deadline) { throw 'The installed desktop did not open a main window with its CLI sidecar.' }
        Start-Sleep -Milliseconds 200
    } while ($true)
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $hadShortcut = Test-Path -LiteralPath $shortcut
    if ($hadShortcut) {
        if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw "The existing Start entry is not a file: $shortcut" }
        Copy-Item -LiteralPath $shortcut -Destination $shortcutBackup
    }
    $shortcutCaptured = $true
    $shortcutRestored = $false
    if ($hadShortcut) { Remove-Item -LiteralPath $shortcut -Force }
    $shell = New-Object -ComObject Shell.Application
    $shortcutShell = New-Object -ComObject WScript.Shell
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
  const name = req.url.replace('/v0.5.1-alpha.4/', '')
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
    # Keep the native shell identity: Known Folder expansion uses USERPROFILE.
    # APPDATA/HOME also stay real. These overrides isolate direct child processes;
    # Explorer-mediated activation may instead use the disposable hosted profile.
    $env:AETHER_CONFIG_DIR = Join-Path $root 'config'
    $env:npm_config_cache = Join-Path $root 'npm-cache'
    $env:ELECTRON_CACHE = Join-Path $root 'electron-cache'
    $env:ELECTRON_BUILDER_CACHE = Join-Path $root 'electron-builder-cache'
    $env:AETHER_NO_UPDATE_CHECK = '1'
    $env:AETHER_BASE_URL = "http://127.0.0.1:$port"
    foreach ($dir in @($env:LOCALAPPDATA, $env:AETHER_CONFIG_DIR)) {
        New-Item -ItemType Directory -Path $dir | Out-Null
    }
    if ($WithoutNode) {
        $env:PATH = (($env:PATH -split ';') | Where-Object {
            $_ -and -not (Test-Path -LiteralPath (Join-Path $_ 'node.exe'))
        }) -join ';'
        if (Get-Command node.exe -ErrorAction SilentlyContinue) { throw 'Node is still on PATH.' }
    }
    $preInstallPath = $env:PATH
    $cliDir = Join-Path $env:LOCALAPPDATA 'Programs\Aether'
    New-Item -ItemType Directory -Path $cliDir -Force | Out-Null
    $sentinel = Join-Path $cliDir 'unrelated.txt'
    'preserve this file' | Set-Content -LiteralPath $sentinel
    $installed = Join-Path $cliDir 'aether.exe'
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'

    foreach ($round in 1..2) {
        & $installer -Version 'v0.5.1-alpha.4'
        if (-not $?) { throw "Automatic install $round failed." }
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash -ne $hash) {
            throw 'Desktop installation changed the CLI.'
        }
        if ((Get-Content -LiteralPath $sentinel) -ne 'preserve this file') { throw 'An unrelated file changed.' }
        if (-not (Test-Path -LiteralPath $desktop)) { throw 'The desktop executable was not installed.' }
        if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw "The Programs Known Folder has no Aether shortcut: $programs" }
        $link = $shortcutShell.CreateShortcut($shortcut)
        if ($link.TargetPath -ine $desktop) { throw "The Start Menu shortcut targets $($link.TargetPath), not $desktop." }
        if ($link.WorkingDirectory -ine (Split-Path -Parent $desktop)) {
            throw "The Start Menu shortcut has the wrong working directory: $($link.WorkingDirectory)"
        }
        & $installed version
        if ($LASTEXITCODE -ne 0) { throw 'The installed CLI no longer runs.' }
        Write-Host "Automatic install $round preserved the CLI and installed the desktop."
    }
    if ($WithoutNode -and -not (Get-ChildItem -LiteralPath (Join-Path $env:LOCALAPPDATA 'aether\node') -Filter node.exe -Recurse)) {
        throw 'The build did not provision its private Node runtime.'
    }

    # AppsFolder's open verb has no argument parameter. Persist only the data
    # isolation flag on the fixture shortcut before the shell catalogues it;
    # leave its installed executable target and working directory unchanged.
    $profile = Join-Path $root 'Electron'
    $link = $shortcutShell.CreateShortcut($shortcut)
    $installedArguments = $link.Arguments
    $catalogueArguments = ($installedArguments + ' --user-data-dir="' + $profile + '"').Trim()
    $link.Arguments = $catalogueArguments
    $link.Save()

    # AppsFolder is the real shell catalogue, not keyboard-driven Start Search.
    # Match its target, not just a stale or unrelated item with the same name.
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    $discovered = $null
    do {
        $candidates = @()
        $appsFolder = $shell.NameSpace('shell:AppsFolder')
        if ($null -eq $appsFolder) { throw 'The shell AppsFolder catalogue is unavailable.' }
        foreach ($item in $appsFolder.Items()) {
            if ($item.Name -inotlike '*Aether*') { continue }
            $target = [string]$item.ExtendedProperty('System.Link.TargetParsingPath')
            $candidates += "Name=$($item.Name); Path=$($item.Path); System.Link.TargetParsingPath=$target"
            if ($item.Name -ieq 'Aether' -and [Environment]::ExpandEnvironmentVariables($target) -ieq $desktop) {
                $discovered = $item
                break
            }
        }
        if ($null -ne $discovered) { break }
        if ([DateTime]::UtcNow -ge $deadline) {
            throw ("Aether was not discovered in the shell AppsFolder catalogue for target $desktop. Programs=$programs. Candidates: " + ($candidates -join ' | '))
        }
        Start-Sleep -Milliseconds 250
    } while ($true)
    # Resolve the matching shortcut too for the separate instrumented launch.
    $programsFolder = $shell.NameSpace($programs)
    if ($null -eq $programsFolder) { throw "The shell Programs folder is unavailable: $programs" }
    $discoveredShortcut = $programsFolder.ParseName('Aether.lnk')
    if ($null -eq $discoveredShortcut) { throw "The shell cannot resolve the catalogued Aether shortcut in $programs." }
    $link = $shortcutShell.CreateShortcut($discoveredShortcut.Path)
    if ($link.TargetPath -ine $desktop -or $link.WorkingDirectory -ine (Split-Path -Parent $desktop)) {
        throw 'The catalogued Aether shortcut no longer identifies the installed desktop and its working directory.'
    }
    if ($link.Arguments -cne $catalogueArguments) { throw 'The catalogued shortcut lost its isolated Electron data directory.' }
    Write-Host "Discovered shell catalogue entry: Name=$($discovered.Name); Path=$($discovered.Path); Target=$($link.TargetPath); Programs=$programs"

    # Model Explorer retaining the PATH from before the installer ran.
    $env:PATH = $preInstallPath
    [Environment]::SetEnvironmentVariable('AETHER_BIN', $null, 'Process')
    $hadProtocol = Test-Path -LiteralPath $protocolKey
    if ($hadProtocol) {
        & $reg export 'HKCU\Software\Classes\aether' $protocolBackup /y
        if ($LASTEXITCODE -ne 0) { throw 'Could not back up the aether:// protocol registration.' }
    }
    $protocolCaptured = $true
    # First launch the exact item returned by AppsFolder, not the .lnk path.
    $discovered.InvokeVerb('open')
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        $launched = @(Get-Process -Name 'aether-desktop' -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -ieq $desktop -and $_.MainWindowHandle -ne [IntPtr]::Zero })
        if ($launched.Count -eq 1) {
            $catalogueApp = $launched[0]
            break
        }
        if ([DateTime]::UtcNow -ge $deadline) { throw 'The AppsFolder open verb did not launch the installed desktop main window.' }
        Start-Sleep -Milliseconds 200
    } while ($true)
    $catalogueChild = Wait-DesktopSidecar $catalogueApp
    if (-not $catalogueApp.CloseMainWindow()) { throw 'The AppsFolder desktop main window could not be closed.' }
    if (-not $catalogueApp.WaitForExit(15000)) { throw 'The AppsFolder desktop did not exit after closing its window.' }
    if (-not $catalogueChild.WaitForExit(15000)) { throw 'The AppsFolder desktop left its CLI sidecar running.' }
    Write-Host 'The exact AppsFolder item opened the installed desktop and closed with its CLI sidecar.'
    $link.Arguments = $installedArguments
    $link.Save()
    $link = $shortcutShell.CreateShortcut($discoveredShortcut.Path)
    if ($link.TargetPath -ine $desktop -or $link.WorkingDirectory -ine (Split-Path -Parent $desktop) -or $link.Arguments -cne $installedArguments) {
        throw 'Restoring the fixture shortcut arguments changed its installed identity.'
    }

    # A second launch supplies CDP instrumentation for the onboarding screenshot.
    # Pass data isolation explicitly too, without relying on shortcut arg merging.
    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $debugPort = $listener.LocalEndpoint.Port
    $listener.Stop()
    $app = Start-Process -FilePath $discoveredShortcut.Path -ArgumentList @("--remote-debugging-port=$debugPort", ('--user-data-dir="' + $profile + '"')) -PassThru
    if ($app.Path -ne $desktop) { throw "The Start Menu shortcut launched $($app.Path), not $desktop." }
    $appChild = Wait-DesktopSidecar $app
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        if ($app.HasExited) { throw "The Start Menu app exited with code $($app.ExitCode)." }
        try {
            $debugInfo = Invoke-RestMethod -Uri "http://127.0.0.1:$debugPort/json/version" -TimeoutSec 2
            $endpoint = $debugInfo.webSocketDebuggerUrl
            break
        } catch {
            if ([DateTime]::UtcNow -gt $deadline) {
                throw "The Start Menu app did not expose its debugging endpoint: $($_.Exception.Message)"
            }
        }
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
    await page.locator('[aria-label="Starting Aether"]').waitFor({ state: 'detached' })
    await page.screenshot({ path: process.argv[4] })
    console.log('The installed desktop rendered its first-run onboarding screen.')
    await page.close()
  } finally {
    await browser.close()
  }
})().catch(error => { console.error(error); process.exitCode = 1 })
'@ | Set-Content -LiteralPath $verifyScript -Encoding UTF8
    $playwright = Join-Path $PSScriptRoot '..\web\node_modules\playwright'
    & $node $verifyScript $playwright $endpoint (Join-Path $env:RUNNER_TEMP 'windows-desktop.png')
    if ($LASTEXITCODE -ne 0) { throw 'The installed desktop did not render onboarding.' }
    if (-not $app.WaitForExit(15000)) { throw 'The desktop did not exit after closing its window.' }
    if (-not $appChild.WaitForExit(15000)) { throw 'The instrumented desktop left its CLI sidecar running.' }
    Write-Host 'The shell-catalogued Aether shortcut found the CLI without an updated PATH and loaded its dashboard.'

    $preferences = Get-MpPreference
    if ((Get-MpComputerStatus).RealTimeProtectionEnabled -ne $true -or $preferences.MAPSReporting -ne 2 -or $preferences.DisableRealtimeMonitoring -or $preferences.DisableIOAVProtection -or $preferences.DisableBehaviorMonitoring) {
        throw 'Defender realtime/cloud protection is not enabled; this would not verify installation safety.'
    }
    if ($preferences.SubmitSamplesConsent -ne 1) {
        throw 'Defender automatic safe sample submission is not enabled after installation.'
    }
    foreach ($flag in $scanningFlags) {
        if ($preferences.$flag) { throw "Installation disabled Defender protection: $flag." }
    }
    foreach ($property in @('ExclusionPath', 'ExclusionProcess', 'ExclusionExtension')) {
        if (($preferences.$property -join "`0") -cne ($initialPreferences.$property -join "`0")) {
            throw "Installation changed Defender $property."
        }
    }
    & "$env:ProgramFiles\Windows Defender\MpCmdRun.exe" -Scan -ScanType 3 -File $root
    if ($LASTEXITCODE -ne 0) { throw "Defender installation scan failed (exit $LASTEXITCODE)." }
    $detections = @(Get-MpThreatDetection | Where-Object { $_.DetectionID -notin $knownDetections })
    if ($detections.Count -gt 0) {
        throw ("Defender detected threats during installation, even if remediated: " + ($detections | Format-List | Out-String))
    }
    Write-Host 'Defender scan of the installer downloads, CLI, Node, and desktop is clean.'
} catch {
    Write-Host $_.ScriptStackTrace
    throw
} finally {
    try {
        try {
            # Match the unique fixture paths, including sidecars orphaned by an
            # early launch failure before either process handle was captured.
            $remaining = @(Get-Process -Name 'aether-desktop', 'aether' -ErrorAction SilentlyContinue |
                Where-Object { ($desktop -and $_.Path -ieq $desktop) -or ($installed -and $_.Path -ieq $installed) })
            foreach ($process in $remaining) {
                if ($process.HasExited) { continue }
                & "$env:SystemRoot\System32\taskkill.exe" /PID $process.Id /T /F | Out-Null
                if (-not $process.WaitForExit(15000)) { throw 'A desktop smoke process did not stop.' }
            }
        } finally {
            if ($server -and -not $server.HasExited) {
                Stop-Process -Id $server.Id -Force
                if (-not $server.WaitForExit(10000)) { throw 'The release fixture server did not stop.' }
            }
        }
    } finally {
        try {
            if ($shortcutCaptured) {
                if ($hadShortcut) {
                    Copy-Item -LiteralPath $shortcutBackup -Destination $shortcut -Force
                } elseif (Test-Path -LiteralPath $shortcut) {
                    Remove-Item -LiteralPath $shortcut -Force
                }
                $shortcutRestored = $true
            }
        } finally {
            foreach ($name in $variables) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
            if ($hadUserPath) { $registry.SetValue('Path', $userPath, $userPathKind) } else { $registry.DeleteValue('Path', $false) }
            $registry.Dispose()
            if ($protocolCaptured) {
                if (Test-Path -LiteralPath $protocolKey) { Remove-Item -LiteralPath $protocolKey -Recurse -Force }
                if ($hadProtocol) {
                    & $reg import $protocolBackup
                    if ($LASTEXITCODE -ne 0) { throw 'Could not restore the aether:// protocol registration.' }
                }
            }
            # Keep the backup and temporary app if restoring the real Start entry failed.
            if ($shortcutRestored -and (Test-Path -LiteralPath $root)) { Remove-Item -LiteralPath $root -Recurse -Force }
        }
    }
}
