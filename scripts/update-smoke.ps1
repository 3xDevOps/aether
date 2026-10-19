[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OldCli,
    [Parameter(Mandatory = $true)][string]$NewCli,
    [Parameter(Mandatory = $true)][string]$FakeRelease,
    # The desktop app built for $OldVersion; the new release publishes it plus a marker file.
    [Parameter(Mandatory = $true)][string]$DesktopZip,
    [Parameter(Mandatory = $true)][string]$OldVersion,
    [Parameter(Mandatory = $true)][string]$NewVersion,
    [Parameter(Mandatory = $true)][string]$Playwright,
    # The latest published GitHub release's CLI, not built from this checkout.
    [Parameter(Mandatory = $true)][string]$PublishedCli,
    [Parameter(Mandatory = $true)][string]$PublishedTag,
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64'
)

# The newest release whose desktop shell lacks AETHER_SHELL_QUITS_ON_76 (desktop/main.js), so it
# cannot update itself on Windows. Releases that include the feature behave differently, so
# do not move this to a newer tag.
$OldShellTag = 'v0.7.0-alpha.16'

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if ($env:OS -ne 'Windows_NT') { throw 'This scenario requires Windows.' }
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This scenario changes machine certificates, the user PATH and the Start Menu, and requires a disposable GitHub-hosted runner.'
}
$initialPreferences = Get-MpPreference
if ((Get-MpComputerStatus).RealTimeProtectionEnabled -ne $true) {
    throw 'Enable Defender realtime protection before running this scenario.'
}
$scanningFlags = @('DisableRealtimeMonitoring', 'DisableIOAVProtection', 'DisableBehaviorMonitoring',
    'DisableScriptScanning', 'DisableArchiveScanning', 'DisableBlockAtFirstSeen')
foreach ($flag in $scanningFlags) {
    if ($initialPreferences.$flag) { throw "Defender $flag is enabled; scanning would be incomplete." }
}
if ($initialPreferences.ExclusionPath -or $initialPreferences.ExclusionProcess -or $initialPreferences.ExclusionExtension) {
    throw 'Run this scenario in a disposable runner without Defender exclusions.'
}
$knownDetections = @(Get-MpThreatDetection | ForEach-Object { $_.DetectionID })
$node = (Get-Command node.exe -CommandType Application | Select-Object -First 1).Source
$installer = Join-Path $PSScriptRoot 'install.ps1'

$root = Join-Path ([IO.Path]::GetTempPath()) ('Aether update ' + [guid]::NewGuid().ToString('N'))
$saved = @{}
foreach ($name in @('LOCALAPPDATA', 'AETHER_CONFIG_DIR', 'HTTPS_PROXY', 'AETHER_NO_UPDATE_CHECK', 'AETHER_BIN',
        'AETHER_BASE_URL', 'AETHER_VERSION', 'AETHER_ROLE', 'AETHER_BIN_DIR')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$releases = @()
$mirror = $null
$taskName = 'aether-daemon'
$exeAsset = "aether-windows-$Arch.exe"
$zipAsset = "aether-desktop-windows-$Arch.zip"
$programs = [Environment]::GetFolderPath([Environment+SpecialFolder]::Programs, [Environment+SpecialFolderOption]::DoNotVerify)
$shortcut = Join-Path $programs 'Aether.lnk'
$proxy = $null

function Invoke-Native([string]$Exe, [string[]]$Arguments, [int]$TimeoutSec = 300) {
    $stem = Join-Path $root ([guid]::NewGuid().ToString('N'))
    $p = Start-Process -FilePath $Exe -ArgumentList $Arguments -PassThru -NoNewWindow `
        -RedirectStandardOutput "$stem.out" -RedirectStandardError "$stem.err"
    $null = $p.Handle
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $p.Id /T /F | Out-Null
        throw "$Exe $Arguments did not finish in ${TimeoutSec}s"
    }
    $p.WaitForExit()
    $result = [pscustomobject]@{
        Code = $p.ExitCode
        Out  = (Get-Content -Raw -LiteralPath "$stem.out")
        Err  = (Get-Content -Raw -LiteralPath "$stem.err")
    }
    Write-Host "> $Exe $Arguments (exit $($result.Code))"
    Write-Host $result.Out
    Write-Host $result.Err
    return $result
}

function Assert([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "ASSERT: $Message" }
    Write-Host "ok: $Message"
}

# Get-FileHash opens without FileShare.Delete, so an update renaming
# aether.exe aside while it is read fails with a sharing violation.
function Get-Hash([string]$Path) {
    $stream = [IO.File]::Open($Path, 'Open', 'Read', 'ReadWrite, Delete')
    try { [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($stream)) -replace '-' }
    finally { $stream.Dispose() }
}

function Get-FreePort {
    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

# A sandbox is a LOCALAPPDATA and config directory of its own, with the old
# CLI at the installer's default path unless $Empty.
function New-Sandbox([string]$Name, [switch]$Empty) {
    $local = Join-Path $root "$Name\Local"
    $env:LOCALAPPDATA = $local
    $env:AETHER_CONFIG_DIR = Join-Path $root "$Name\config"
    New-Item -ItemType Directory -Path $local, $env:AETHER_CONFIG_DIR | Out-Null
    $dir = Join-Path $local 'Programs\Aether'
    if ($Empty) { return Join-Path $dir 'aether.exe' }
    New-Item -ItemType Directory -Path $dir | Out-Null
    Copy-Item -LiteralPath $OldCli -Destination (Join-Path $dir 'aether.exe')
    return Join-Path $dir 'aether.exe'
}

function Get-ProcessAt([string]$Name, [string]$Path) {
    @(Get-Process -Name $Name -ErrorAction SilentlyContinue | Where-Object { $_.Path -ieq $Path })
}

function Start-Holder([string]$Exe) {
    $log = Join-Path $root ([guid]::NewGuid().ToString('N') + '.url')
    $p = Start-Process -FilePath $Exe -ArgumentList 'gui', '--url' -PassThru -WindowStyle Hidden -RedirectStandardOutput $log -RedirectStandardError "$log.err"
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        $url = Get-Content -LiteralPath $log -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($url) { return [pscustomobject]@{ Process = $p; Url = $url; Started = $p.StartTime } }
        if ($p.HasExited) { throw "aether gui exited: $(Get-Content -Raw -LiteralPath "$log.err")" }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'aether gui did not print its URL.'
}

function Test-Holder($Holder) {
    $Holder.Process.Refresh()
    if ($Holder.Process.HasExited) { return $false }
    $response = Invoke-WebRequest -Uri $Holder.Url -UseBasicParsing -TimeoutSec 10
    return $response.StatusCode -eq 200 -and $Holder.Process.StartTime -eq $Holder.Started
}

function Set-Slow([string]$Delay) {
    Invoke-WebRequest -Uri "http://$proxy/slow?d=$Delay" -UseBasicParsing | Out-Null
}

function Assert-NoNewDetections([string]$When) {
    $detections = @(Get-MpThreatDetection | Where-Object { $_.DetectionID -notin $knownDetections })
    if ($detections.Count -gt 0) {
        throw ("Defender detected threats $When, even if remediated: " + ($detections | Format-List | Out-String))
    }
    Write-Host "ok: Get-MpThreatDetection has no new detection $When"
}

# The updater pins https://github.com. A fake-release instance answers
# CONNECT github.com:443 for it (HTTPS_PROXY) with a certificate from a
# throwaway CA, which only this machine trusts, in LocalMachine\Root.
function Start-Release([string]$Name, [string]$Assets, [string]$Latest) {
    $log = Join-Path $root "$Name-release.out"
    $ca = Join-Path $root "$Name-ca.pem"
    $p = Start-Process -FilePath $FakeRelease -PassThru -WindowStyle Hidden `
        -ArgumentList '-assets', ('"' + $Assets + '"'), '-latest', $Latest, '-ca-out', ('"' + $ca + '"') `
        -RedirectStandardOutput $log -RedirectStandardError "$log.err"
    $script:releases += $p
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($p.HasExited) { throw "fake-release exited: $(Get-Content -Raw -LiteralPath "$log.err")" }
        $address = Get-Content -LiteralPath $log -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($address) { break }
        if ([DateTime]::UtcNow -gt $deadline) { throw 'fake-release did not start.' }
        Start-Sleep -Milliseconds 100
    } while ($true)
    & certutil.exe -addstore -f Root $ca | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Could not trust the fake release CA.' }
    return [pscustomobject]@{ Address = $address }
}

function Use-Release($Release) {
    $env:HTTPS_PROXY = "http://$($Release.Address)"
    $script:proxy = $Release.Address
}

function Get-DaemonState {
    $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($task) { return [string]$task.State }
    return ''
}

function Wait-DaemonState([string]$State, [int]$TimeoutSec) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    do {
        if ((Get-DaemonState) -eq $State) { return $true }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    return $false
}

# The Scheduled Task aether daemon install registers: syncd.scheduledTask's
# settings, with a stand-in for the daemon's command.
function New-DaemonTask {
    $xml = Join-Path $root 'aether-daemon.xml'
    @'
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Aether git sync daemon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Settings>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>10</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>cmd.exe</Command>
      <Arguments>/c ping -n 3000 127.0.0.1 &gt; nul</Arguments>
    </Exec>
  </Actions>
</Task>
'@ | Set-Content -LiteralPath $xml -Encoding Unicode
    & schtasks.exe /Create /TN $taskName /XML $xml /F
    if ($LASTEXITCODE -ne 0) { throw 'Could not register the stand-in aether-daemon task from the daemon''s task XML.' }
    & schtasks.exe /Run /TN $taskName
    if ($LASTEXITCODE -ne 0 -or -not (Wait-DaemonState 'Running' 30)) { throw 'The stand-in aether-daemon task did not start.' }
}

function Start-App([string]$Target, [string]$Desktop, [string]$Profile, [int]$Port) {
    $known = @(Get-Process -Name 'aether-desktop' -ErrorAction SilentlyContinue | ForEach-Object { $_.Id })
    $arguments = @("--remote-debugging-port=$Port", ('--user-data-dir="' + $Profile + '"'))
    if ($Target -like '*.lnk') {
        $null = Start-Process -FilePath $Target -ArgumentList $arguments
    } else {
        # The Start Menu shortcut starts the app in its own directory, and the
        # gateway and the update helper inherit it.
        $null = Start-Process -FilePath $Target -WorkingDirectory (Split-Path -Parent $Desktop) -ArgumentList $arguments
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    do {
        $main = @(Get-ProcessAt 'aether-desktop' $Desktop | Where-Object { $_.Id -notin $known -and $_.MainWindowHandle -ne [IntPtr]::Zero })
        if ($main.Count -gt 0) { return $main[0] }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "$Target did not open a desktop window."
}

# The sidecar is the app's child running $Cli, or $Cli renamed beside itself.
function Wait-Sidecar([Diagnostics.Process]$Process, [string]$Cli, [int]$NotPid = 0) {
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    do {
        $Process.Refresh()
        if ($Process.HasExited) { throw "The desktop exited before it had a CLI sidecar (code $($Process.ExitCode))." }
        $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $($Process.Id)" |
            Where-Object { $_.ProcessId -ne $NotPid -and $_.ExecutablePath -and $_.ExecutablePath -like "$Cli*" })
        if ($children.Count -eq 1) { return $children[0].ProcessId }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "The desktop (pid $($Process.Id)) has no sidecar running $Cli."
}

function Stop-App([Diagnostics.Process]$Process) {
    if ($Process.HasExited) { return }
    $null = $Process.CloseMainWindow()
    if (-not $Process.WaitForExit(20000)) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $Process.Id /T /F | Out-Null
        $null = $Process.WaitForExit(15000)
    }
}

function Wait-Relaunch([string]$Desktop, [int]$OldId, [int]$TimeoutSec = 240) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    do {
        $relaunched = @(Get-ProcessAt 'aether-desktop' $Desktop | Where-Object { $_.Id -ne $OldId -and $_.MainWindowHandle -ne [IntPtr]::Zero })
        if ($relaunched.Count -gt 0) { return $relaunched[0] }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Filter last-error.txt -ErrorAction SilentlyContinue |
        ForEach-Object { Write-Host "$($_.FullName): $(Get-Content -Raw -LiteralPath $_.FullName)" }
    Get-Process -Name 'aether', 'aether-desktop' -ErrorAction SilentlyContinue | Format-Table Id, Path -AutoSize | Out-String | Write-Host
    throw "The updated desktop app was not started within ${TimeoutSec}s."
}

function Get-LastError {
    $files = @(Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Filter last-error.txt -ErrorAction SilentlyContinue)
    return ($files | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName }) -join "`n"
}

# Reads what the running desktop's dashboard shows through the CDP port:
# 'probe' proves the gateway answers, 'click' drives the footer's Update
# link and the Updates dialog's Update now button.
$appScript = Join-Path $PSScriptRoot 'update-smoke-app.cjs'
function Invoke-App([string]$Mode, [int]$Port, [int]$TimeoutSec = 180) {
    Invoke-Native $node @(('"' + $appScript + '"'), ('"' + $Playwright + '"'), $Mode, "http://127.0.0.1:$Port") $TimeoutSec
}

function Assert-ReleaseResources([string]$Path, [string]$What) {
    $info = (Get-Item -LiteralPath $Path).VersionInfo
    Assert ($info.ProductName -eq 'Aether' -and $info.CompanyName -eq '3xDevOps' -and $info.FileVersion -eq '0.5.1.0') "$What carries the release VERSIONINFO ($($info.CompanyName) / $($info.ProductName) / $($info.FileVersion))"
    $text = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($Path))
    Assert ($text.Contains('requestedExecutionLevel') -and $text.Contains('io.aether.client')) "$What carries the release application manifest"
}

function Start-Mirror {
    $dir = Join-Path $root 'mirror'
    New-Item -ItemType Directory -Path $dir | Out-Null
    Copy-Item -LiteralPath $OldCli -Destination (Join-Path $dir $exeAsset)
    [IO.File]::WriteAllText((Join-Path $dir 'checksums.txt'), "$(Get-Hash $OldCli)  $exeAsset`n")
    $script = Join-Path $root 'mirror.cjs'
    @'
const http = require('node:http')
const fs = require('node:fs')
const path = require('node:path')
const [root, tag, ...names] = process.argv.slice(2)
const files = new Set(names)
const server = http.createServer((req, res) => {
  const name = req.url.replace('/' + tag + '/', '')
  if (!files.has(name)) { res.writeHead(404); res.end(); return }
  const file = path.join(root, name)
  res.writeHead(200, { 'Content-Length': fs.statSync(file).size })
  fs.createReadStream(file).pipe(res)
})
server.listen(0, '127.0.0.1', () => console.log(server.address().port))
'@ | Set-Content -LiteralPath $script -Encoding UTF8
    $log = Join-Path $root 'mirror.log'
    $p = Start-Process -FilePath $node -PassThru -ArgumentList @(('"' + $script + '"'), ('"' + $dir + '"'), $OldVersion, $exeAsset, 'checksums.txt') `
        -RedirectStandardOutput $log -RedirectStandardError "$log.err"
    $script:mirror = $p
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($p.HasExited) { throw 'The release mirror exited.' }
        $port = Get-Content -LiteralPath $log -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($port) { return $port }
        if ([DateTime]::UtcNow -gt $deadline) { throw 'The release mirror did not start.' }
        Start-Sleep -Milliseconds 100
    } while ($true)
}

# scripts/install.ps1 as a user runs it, into the default directories.
function Invoke-Installer([string]$Port, [hashtable]$Extra) {
    $env:AETHER_BASE_URL = "http://127.0.0.1:$Port"
    $env:AETHER_NO_UPDATE_CHECK = '1'
    foreach ($name in @('AETHER_BIN', 'AETHER_VERSION', 'AETHER_ROLE', 'AETHER_BIN_DIR')) {
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    & $installer -Version $OldVersion @Extra
    if (-not $?) { throw 'scripts/install.ps1 failed.' }
    [Environment]::SetEnvironmentVariable('AETHER_BASE_URL', $null, 'Process')
    [Environment]::SetEnvironmentVariable('AETHER_NO_UPDATE_CHECK', $null, 'Process')
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    foreach ($dir in @('assets', 'old-assets', 'bad-assets')) { New-Item -ItemType Directory -Path (Join-Path $root $dir) | Out-Null }
    $assets = Join-Path $root 'assets'
    $newAsset = Join-Path $assets $exeAsset
    Copy-Item -LiteralPath $NewCli -Destination $newAsset
    $newZip = Join-Path $assets $zipAsset
    Copy-Item -LiteralPath $DesktopZip -Destination $newZip
    Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::Open($newZip, 'Update')
    try {
        $entry = $archive.CreateEntry('new-release-marker.txt')
        $writer = New-Object IO.StreamWriter($entry.Open())
        $writer.Write($NewVersion)
        $writer.Dispose()
    } finally { $archive.Dispose() }
    # The old release the real installer installs from: the old desktop, no marker.
    Copy-Item -LiteralPath $OldCli -Destination (Join-Path $root "old-assets\$exeAsset")
    Copy-Item -LiteralPath $DesktopZip -Destination (Join-Path $root "old-assets\$zipAsset")
    # A release whose CLI asset the system cannot start: signed and listed in
    # the manifest, but not a PE file.
    [IO.File]::WriteAllText((Join-Path $root "bad-assets\$exeAsset"), 'MZ this is not a portable executable')
    $oldHash = Get-Hash $OldCli
    $newHash = Get-Hash $NewCli
    Assert ($oldHash -ne $newHash) 'the old and new builds differ'
    Assert-ReleaseResources $OldCli 'the old CLI'
    Assert-ReleaseResources $NewCli 'the new CLI'

    $newRelease = Start-Release 'new' $assets $NewVersion
    $oldRelease = Start-Release 'old' (Join-Path $root 'old-assets') $OldVersion
    $badRelease = Start-Release 'bad' (Join-Path $root 'bad-assets') $NewVersion
    Use-Release $newRelease
    [Environment]::SetEnvironmentVariable('AETHER_NO_UPDATE_CHECK', $null, 'Process')
    [Environment]::SetEnvironmentVariable('AETHER_BIN', $null, 'Process')

    Write-Host '=== CLI update with the old process running ==='
    $installed = New-Sandbox 'cli'
    $cliDir = Split-Path -Parent $installed
    $old = "$installed.old"
    $sentinel = Join-Path $cliDir 'unrelated.txt'
    'preserve this file' | Set-Content -LiteralPath $sentinel
    $version = Invoke-Native $installed @('version')
    Assert ($version.Out -match [regex]::Escape($OldVersion)) "the installed build reports $OldVersion"
    $check = Invoke-Native $installed @('update', '--check')
    Assert ($check.Code -eq 0 -and $check.Out -match [regex]::Escape($NewVersion)) "aether update --check sees $NewVersion through the fake release"

    $holder = Start-Holder $installed
    Assert (Test-Holder $holder) 'the old aether.exe is running and serving its gateway'

    # An update killed while it downloads: the staged file stays, nothing
    # else changes, and the next start runs the build that was installed.
    Set-Slow '120s'
    $killed = Start-Process -FilePath $installed -ArgumentList 'update' -PassThru -WindowStyle Hidden
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    do {
        $staged = @(Get-ChildItem -LiteralPath $cliDir -Force | Where-Object { $_.Name -like '.aether.exe.update-*' })
        if ($staged.Count -gt 0 -or $killed.HasExited) { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    & "$env:SystemRoot\System32\taskkill.exe" /PID $killed.Id /T /F | Out-Null
    Set-Slow '0s'
    $null = $killed.WaitForExit(15000)
    Assert ($staged.Count -gt 0) 'the update had begun staging its download when it was killed'
    Assert ((Get-Hash $installed) -eq $oldHash) 'the update killed mid-download left aether.exe the old build'
    Assert (-not (Test-Path -LiteralPath $old)) 'the update killed mid-download made no aether.exe.old'
    $version = Invoke-Native $installed @('version')
    Assert ($version.Code -eq 0 -and $version.Out -match [regex]::Escape($OldVersion)) 'the next start after the killed update runs the old build'
    $leftStaged = @(Get-ChildItem -LiteralPath $cliDir -Force | Where-Object { $_.Name -like '.aether.exe.update-*' })
    Write-Host "A killed download leaves its staged file beside aether.exe: $($leftStaged.Count) file(s), $(($leftStaged | ForEach-Object { '{0} ({1} bytes)' -f $_.Name, $_.Length }) -join ', ')"
    $leftStaged | Remove-Item -Force
    Assert (Test-Holder $holder) 'the old process survived the killed update'

    # A staged download another process holds open makes the second rename
    # fail: the first rename (aether.exe -> aether.exe.old) has already
    # happened by then.
    $locker = Join-Path $root 'locker.ps1'
    @'
param($Dir, $Ready, $Release)
$deadline = [DateTime]::UtcNow.AddSeconds(120)
while ([DateTime]::UtcNow -lt $deadline) {
    $file = Get-ChildItem -LiteralPath $Dir -Filter '.aether.exe.update-*' -Force -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($file) {
        try {
            $stream = [IO.File]::Open($file.FullName, 'Open', 'Read', 'ReadWrite')
            Set-Content -LiteralPath $Ready -Value $file.FullName
            while (-not (Test-Path -LiteralPath $Release)) { Start-Sleep -Milliseconds 100 }
            $stream.Dispose()
            exit 0
        } catch { }
    }
    Start-Sleep -Milliseconds 20
}
exit 1
'@ | Set-Content -LiteralPath $locker -Encoding UTF8
    $ready = Join-Path $root 'locked.txt'
    $release = Join-Path $root 'release-lock.txt'
    $lockProcess = Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -PassThru -WindowStyle Hidden `
        -ArgumentList '-NoProfile', '-File', ('"' + $locker + '"'), ('"' + $cliDir + '"'), ('"' + $ready + '"'), ('"' + $release + '"')
    Set-Slow '10s'
    $failed = Invoke-Native $installed @('update')
    Set-Slow '0s'
    Assert (Test-Path -LiteralPath $ready) 'the staged download was held open during the update'
    Assert ($failed.Code -ne 0) 'aether update fails when the second rename fails'
    Assert (($failed.Err + $failed.Out) -match 'replace .*aether\.exe') 'the failure names the replacement it could not make'
    Assert ((Get-Hash $installed) -eq $oldHash) 'the failed update rolled aether.exe back to the old build'
    Assert (-not (Test-Path -LiteralPath $old)) 'the rollback left no aether.exe.old'
    Assert (Test-Holder $holder) 'the old process survived the failed update'
    New-Item -ItemType File -Path $release | Out-Null
    $null = $lockProcess.WaitForExit(30000)
    # Discard cannot remove a file another process held open, so the held
    # download outlives the failed attempt; clear it for the next checks.
    $held = @(Get-ChildItem -LiteralPath $cliDir -Force | Where-Object { $_.Name -like '.aether.exe.update-*' })
    Write-Host "Staged downloads left by the held-open attempt: $($held.Count)"
    $held | Remove-Item -Force
    Assert ((Get-Content -LiteralPath $sentinel) -eq 'preserve this file') 'an unrelated file in the install directory is untouched'

    $updated = Invoke-Native $installed @('update')
    Assert ($updated.Code -eq 0) 'aether update succeeds against the fake release'
    Assert ((Get-Hash $installed) -eq $newHash) 'aether.exe is the new build'
    Assert-ReleaseResources $installed 'the swapped-in aether.exe'
    Assert (Test-Path -LiteralPath $old) 'the running old image was renamed to aether.exe.old'
    Assert ((Get-Hash $old) -eq $oldHash) 'aether.exe.old is the old build'
    Assert (Test-Holder $holder) 'the old process was undisturbed: same process, same start time, gateway still answering'
    $holderImage = (Get-CimInstance Win32_Process -Filter "ProcessId = $($holder.Process.Id)").ExecutablePath
    Write-Host "The old process now reports its image as $holderImage"
    $version = Invoke-Native $installed @('version')
    Assert ($version.Out -match [regex]::Escape($NewVersion)) "the installed build now reports $NewVersion"
    $leftovers = @(Get-ChildItem -LiteralPath $cliDir -Force | Where-Object { $_.Name -like '.aether.exe.update-*' })
    Assert ($leftovers.Count -eq 0) 'no staged download is left beside aether.exe'
    Assert ((Get-Content -LiteralPath $sentinel) -eq 'preserve this file') 'an unrelated file in the install directory is still untouched'

    $again = Invoke-Native $installed @('update', '--version', 'v9.9.9')
    Assert ($again.Code -eq 0) 'a second update succeeds while the first old image still runs'
    Assert (Test-Holder $holder) 'the old process survived the second update'

    $holder.Process.Kill()
    $null = $holder.Process.WaitForExit(15000)
    $null = Invoke-Native $installed @('version')
    Assert (@(Get-ChildItem -LiteralPath $cliDir -Force -Filter 'aether.exe.old*').Count -eq 0) 'the next start removed every aether.exe.old'
    Assert ((Get-Hash $installed) -ne $oldHash) 'aether.exe is still the new build after the cleanup'

    # A release whose binary Windows will not start (here: not a PE file;
    # on a user's machine Smart App Control, AppLocker or antivirus) must
    # not replace the working CLI.
    $working = Get-Hash $installed
    Use-Release $badRelease
    $blocked = Invoke-Native $installed @('update', '--version', 'v9.9.8')
    Use-Release $newRelease
    Assert ($blocked.Code -ne 0) 'aether update fails when the new aether.exe does not start'
    Assert (($blocked.Err + $blocked.Out) -match 'does not start, so the previous build was restored') 'the failure says the new build does not start and the previous one was restored'
    Assert ((Get-Hash $installed) -eq $working) 'the blocked update left the working aether.exe in place'
    Assert (@(Get-ChildItem -LiteralPath $cliDir -Force | Where-Object { $_.Name -like 'aether.exe.*' -or $_.Name -like '.aether.exe.update-*' }).Count -eq 0) 'the blocked update left no aether.exe.old or staged file'
    $version = Invoke-Native $installed @('version')
    Assert ($version.Code -eq 0) 'aether still runs after the blocked update'

    # A process killed between swapAside's two renames leaves no aether.exe
    # and the running build as aether.exe.old: the state is made here, the
    # window being too short to hit.
    Move-Item -LiteralPath $installed -Destination $old
    Assert (-not (Test-Path -LiteralPath $installed)) 'aether.exe is missing after a swap killed between its renames'
    $threw = $false
    try { & $installed version } catch { $threw = $true; Write-Host "The shell's answer: $($_.Exception.Message)" }
    Assert $threw 'running aether.exe fails until aether.exe.old is moved back'
    Move-Item -LiteralPath $old -Destination $installed -Force
    $version = Invoke-Native $installed @('version')
    Assert ($version.Code -eq 0) 'Move-Item of aether.exe.old over aether.exe restores the CLI'
    Assert-NoNewDetections 'after the CLI update'

    Write-Host '=== real installer, Update now button, daemon task and Start Menu shortcut ==='
    $mirrorPort = Start-Mirror
    $installed = New-Sandbox 'real' -Empty
    $cliDir = Split-Path -Parent $installed
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $appDir = Split-Path -Parent $desktop
    Use-Release $oldRelease
    Invoke-Installer $mirrorPort @{}
    Use-Release $newRelease
    Assert ((Get-Hash $installed) -eq $oldHash) 'scripts/install.ps1 installed the old CLI at the default path'
    Assert (Test-Path -LiteralPath $desktop) 'scripts/install.ps1 installed the old desktop app at the default path'
    Assert (-not (Test-Path -LiteralPath (Join-Path $appDir 'new-release-marker.txt'))) 'the installed app is the old one'
    Assert ([Environment]::GetEnvironmentVariable('Path', 'User') -split ';' -contains $cliDir) 'the installer put the CLI directory on the user PATH'
    Assert (Test-Path -LiteralPath $shortcut -PathType Leaf) 'the installer created the Start Menu shortcut'
    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut($shortcut)
    Assert ($link.TargetPath -ieq $desktop -and $link.WorkingDirectory -ieq $appDir) 'the shortcut targets the installed app and starts it in its directory'

    # Does the task come back on its own after the helper's schtasks /End?
    New-DaemonTask
    & schtasks.exe /End /TN $taskName
    Write-Host 'Waiting 75s to see whether the aether-daemon task restarts by itself after schtasks /End...'
    Start-Sleep -Seconds 75
    $selfRestart = Get-DaemonState
    Write-Host "75s after schtasks /End the aether-daemon task is: $selfRestart (RestartOnFailure=PT1M x10)"
    & schtasks.exe /Run /TN $taskName
    Assert (Wait-DaemonState 'Running' 30) 'the stand-in aether-daemon task is running before the update'

    $electron = Join-Path $root 'Electron'
    $port = Get-FreePort
    $app = Start-App $shortcut $desktop $electron $port
    $appId = $app.Id
    $sidecarId = Wait-Sidecar $app $installed
    Write-Host "The old desktop (pid $appId) runs its sidecar (pid $sidecarId) from the old CLI."
    $probe = Invoke-App 'probe' $port
    Assert ($probe.Code -eq 0) 'the old dashboard answers through the running app'

    $clicked = Invoke-App 'click' $port 300
    Assert ($clicked.Out -match 'clicked Update now') 'the Update now button in the running app was clicked'

    $relaunched = Wait-Relaunch $desktop $appId
    Write-Host "ok: a new desktop process (pid $($relaunched.Id)) was started from the app directory"
    Assert ((Get-Process -Id $appId -ErrorAction SilentlyContinue) -eq $null) 'the old desktop process exited'
    Assert ((Get-Process -Id $sidecarId -ErrorAction SilentlyContinue) -eq $null) 'the old sidecar exited'
    Assert ((Get-Content -Raw -LiteralPath (Join-Path $appDir 'new-release-marker.txt')).Trim() -eq $NewVersion) 'the app directory holds the new release'
    Assert ((Get-Hash $installed) -eq $newHash) 'the CLI the app runs is the new build'
    Assert (-not (Get-LastError)) 'no desktop build error was recorded'
    Assert (-not (Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Directory -Filter staged-app -ErrorAction SilentlyContinue)) 'the staged app was consumed'
    $newSidecar = Wait-Sidecar $relaunched $installed
    Write-Host "ok: the relaunched desktop runs a new sidecar (pid $newSidecar)"
    Assert (Wait-DaemonState 'Running' 60) 'the helper started the aether-daemon task again after the swap'
    $link = $shell.CreateShortcut($shortcut)
    Assert ($link.TargetPath -ieq $desktop) 'the Start Menu shortcut still targets the app directory'

    Stop-App $relaunched
    $fromShortcut = Start-App $shortcut $desktop $electron (Get-FreePort)
    $shortcutSidecar = Wait-Sidecar $fromShortcut $installed
    Assert (Test-Path -LiteralPath (Join-Path (Split-Path -Parent $fromShortcut.Path) 'new-release-marker.txt')) 'the Start Menu shortcut launches the new app'
    Write-Host "ok: the shortcut's app (pid $($fromShortcut.Id)) runs sidecar pid $shortcutSidecar from the new CLI"
    Stop-App $fromShortcut
    & schtasks.exe /End /TN $taskName | Out-Null
    Assert-NoNewDetections 'after the desktop update'

    Write-Host '=== aether update from a terminal while the desktop app runs ==='
    $installed = New-Sandbox 'terminal'
    $cliDir = Split-Path -Parent $installed
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $appDir = Split-Path -Parent $desktop
    $install = Invoke-Native $installed @('gui', 'build', '--zip', ('"' + $DesktopZip + '"')) 600
    Assert ($install.Code -eq 0 -and (Test-Path -LiteralPath $desktop)) 'the old desktop app is installed'
    $port = Get-FreePort
    $app = Start-App $desktop $desktop $electron $port
    $sidecarId = Wait-Sidecar $app $installed
    $terminal = Invoke-Native $installed @('update') 600
    Assert ((Get-Hash $installed) -eq $newHash) 'the terminal update replaced aether.exe while the app ran from it'
    Assert (Test-Path -LiteralPath "$installed.old") 'the running sidecar image was renamed to aether.exe.old'
    Assert ($terminal.Code -ne 0 -and ($terminal.Err + $terminal.Out) -match 'aether is updated, but the desktop app was not rebuilt') 'the terminal update says the CLI is updated but the running app was not rebuilt'
    Write-Host "The terminal update's message about the running app: $(($terminal.Err + $terminal.Out).Trim())"
    $app.Refresh()
    Assert (-not $app.HasExited -and (Get-Process -Id $sidecarId -ErrorAction SilentlyContinue)) 'the app and its old sidecar kept running'
    Assert (-not (Test-Path -LiteralPath (Join-Path $appDir 'new-release-marker.txt'))) 'the running app directory was not touched'
    $probe = Invoke-App 'probe' $port
    Assert ($probe.Code -eq 0) 'the old sidecar still serves the dashboard from aether.exe.old'
    Stop-App $app
    Assert ((Get-Process -Id $sidecarId -ErrorAction SilentlyContinue) -eq $null) 'the old sidecar exited with the app'
    $rebuild = Invoke-Native $installed @('gui', 'build', '--zip', ('"' + $newZip + '"')) 600
    Assert ($rebuild.Code -eq 0 -and (Test-Path -LiteralPath (Join-Path $appDir 'new-release-marker.txt'))) 'the rebuild the message names installs the new app once the app is closed'
    $app = Start-App $desktop $desktop $electron (Get-FreePort)
    $null = Wait-Sidecar $app $installed
    Stop-App $app
    $null = Invoke-Native $installed @('version')
    Assert (@(Get-ChildItem -LiteralPath $cliDir -Force -Filter 'aether.exe.old*').Count -eq 0) 'the next start removed aether.exe.old once the old sidecar was gone'

    Write-Host '=== CLI-only install (role none), no desktop app ==='
    $installed = New-Sandbox 'clionly' -Empty
    $cliDir = Split-Path -Parent $installed
    Use-Release $oldRelease
    Invoke-Installer $mirrorPort @{ Role = 'none' }
    Use-Release $newRelease
    Assert ((Get-Hash $installed) -eq $oldHash) 'scripts/install.ps1 -Role none installed the old CLI at the default path'
    Assert (-not (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop'))) 'no desktop app is installed'
    $only = Invoke-Native $installed @('update') 600
    Assert ($only.Code -eq 0) 'aether update succeeds on a CLI-only install'
    Assert ((Get-Hash $installed) -eq $newHash) 'the CLI-only install is the new build'
    Assert (-not (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop'))) 'aether update installed no desktop app'
    Assert (-not (Get-LastError)) 'no desktop build error was recorded'

    Write-Host "=== the $OldShellTag desktop shell with the new CLI ==="
    $oldShellDir = Join-Path $root 'old-shell'
    New-Item -ItemType Directory -Path $oldShellDir -Force | Out-Null
    $oldShellZip = Join-Path $oldShellDir "aether-desktop-windows-$Arch.zip"
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/$env:GITHUB_REPOSITORY/releases/download/$OldShellTag/aether-desktop-windows-$Arch.zip" -OutFile $oldShellZip
    $installed = New-Sandbox 'oldshell'
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $appDir = Split-Path -Parent $desktop
    $install = Invoke-Native $installed @('gui', 'build', '--zip', ('"' + $oldShellZip + '"')) 600
    Assert ($install.Code -eq 0 -and (Test-Path -LiteralPath $desktop)) "the $OldShellTag desktop app is installed"
    $port = Get-FreePort
    $app = Start-App $desktop $desktop $electron $port
    $appId = $app.Id
    $sidecarId = Wait-Sidecar $app $installed
    $probe = Invoke-App 'probe' $port
    Assert ($probe.Code -eq 0) "the $OldShellTag shell shows the dashboard of the CLI it runs"
    $clicked = Invoke-App 'click' $port 300
    Assert ($clicked.Out -match 'clicked Update now') "Update now was clicked in the $OldShellTag shell"
    $deadline = [DateTime]::UtcNow.AddSeconds(240)
    while ((Get-Hash $installed) -ne $newHash -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 500 }
    Assert ((Get-Hash $installed) -eq $newHash) "the CLI was updated from the $OldShellTag shell"
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    while (-not (Get-LastError) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 500 }
    $recorded = Get-LastError
    Write-Host "Recorded build error: $recorded"
    Assert ($recorded -match 'too old to update itself on Windows' -and $recorded -match 'aether gui build') 'the documented error names the old shell and the command to run'
    $app.Refresh()
    Assert (-not $app.HasExited) "the $OldShellTag app is still running"
    Assert (-not (Test-Path -LiteralPath (Join-Path $appDir 'new-release-marker.txt'))) 'the old app directory was left alone'
    $respawned = Wait-Sidecar $app $installed $sidecarId
    Write-Host "ok: the old shell respawned its sidecar (pid $respawned) on the new CLI"
    $probe = Invoke-App 'probe' $port
    Assert ($probe.Code -eq 0) "the $OldShellTag shell still shows the dashboard on the new CLI"
    Stop-App $app
    $app = Start-App $desktop $desktop $electron (Get-FreePort)
    $null = Wait-Sidecar $app $installed
    Write-Host "ok: the $OldShellTag app launches again on the new CLI"
    Stop-App $app

    Write-Host '=== the new desktop shell with the latest published CLI ==='
    $installed = New-Sandbox 'newshell'
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $install = Invoke-Native $installed @('gui', 'build', '--zip', ('"' + $newZip + '"')) 600
    Assert ($install.Code -eq 0 -and (Test-Path -LiteralPath $desktop)) 'the new desktop app is installed'
    Copy-Item -LiteralPath $PublishedCli -Destination $installed -Force
    $version = Invoke-Native $installed @('version')
    Assert ($version.Out -match [regex]::Escape($PublishedTag)) "the CLI beside the new app is the published $PublishedTag"
    $port = Get-FreePort
    $app = Start-App $desktop $desktop $electron $port
    $null = Wait-Sidecar $app $installed
    $probe = Invoke-App 'probe' $port
    Assert ($probe.Code -eq 0) "the new shell shows the dashboard of the published $PublishedTag CLI"
    Stop-App $app
    Assert-NoNewDetections 'after the shell and CLI pairings'

    $preferences = Get-MpPreference
    if ((Get-MpComputerStatus).RealTimeProtectionEnabled -ne $true) { throw 'Defender realtime protection was turned off during the scenario.' }
    foreach ($flag in $scanningFlags) {
        if ($preferences.$flag) { throw "The scenario disabled Defender protection: $flag." }
    }
    foreach ($property in @('ExclusionPath', 'ExclusionProcess', 'ExclusionExtension')) {
        if (($preferences.$property -join "`0") -cne ($initialPreferences.$property -join "`0")) {
            throw "The scenario changed Defender $property."
        }
    }
    Write-Host 'Defender real-time protection stayed on with no exclusions through every update.'
} catch {
    Write-Host $_.ScriptStackTrace
    & "$PSScriptRoot/defender-diagnostics.ps1"
    throw
} finally {
    $ErrorActionPreference = 'Continue'
    try {
        foreach ($process in @(Get-Process -Name 'aether-desktop', 'aether' -ErrorAction SilentlyContinue |
                Where-Object { $_.Path -and $_.Path -like "$root*" })) {
            & "$env:SystemRoot\System32\taskkill.exe" /PID $process.Id /T /F | Out-Null
        }
        foreach ($process in $releases + @($mirror)) {
            if ($process -and -not $process.HasExited) { $process.Kill() }
        }
        & schtasks.exe /End /TN $taskName 2>&1 | Out-Null
        & schtasks.exe /Delete /TN $taskName /F 2>&1 | Out-Null
        & certutil.exe -delstore Root 'Aether update smoke CA' 2>&1 | Out-Null
        foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    } catch {
        Write-Host "cleanup: $($_.Exception.Message)"
    }
}
