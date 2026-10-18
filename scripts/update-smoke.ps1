[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$OldCli,
    [Parameter(Mandatory = $true)][string]$NewCli,
    [Parameter(Mandatory = $true)][string]$FakeRelease,
    # The desktop app built for $OldVersion; the new release publishes it plus a marker file.
    [Parameter(Mandatory = $true)][string]$DesktopZip,
    [Parameter(Mandatory = $true)][string]$OldVersion,
    [Parameter(Mandatory = $true)][string]$NewVersion,
    [Parameter(Mandatory = $true)][string]$Playwright
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$PSNativeCommandUseErrorActionPreference = $false
if ($env:OS -ne 'Windows_NT') { throw 'This scenario requires Windows.' }
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This scenario changes machine certificates and requires a disposable GitHub-hosted runner.'
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

$root = Join-Path ([IO.Path]::GetTempPath()) ('Aether update ' + [guid]::NewGuid().ToString('N'))
$saved = @{}
foreach ($name in @('LOCALAPPDATA', 'AETHER_CONFIG_DIR', 'HTTPS_PROXY', 'AETHER_NO_UPDATE_CHECK', 'AETHER_BIN')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$fake = $null
$caSubject = 'Aether update smoke CA'
$taskName = 'aether-daemon'

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

function Get-Hash([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash }

function New-Sandbox([string]$Name) {
    $local = Join-Path $root "$Name\Local"
    $env:LOCALAPPDATA = $local
    $env:AETHER_CONFIG_DIR = Join-Path $root "$Name\config"
    $dir = Join-Path $local 'Programs\Aether'
    New-Item -ItemType Directory -Path $dir, $env:AETHER_CONFIG_DIR | Out-Null
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

function Wait-DesktopSidecar([Diagnostics.Process]$Process, [string]$Cli) {
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    do {
        $Process.Refresh()
        if ($Process.HasExited) { throw "The desktop exited before opening its main window (code $($Process.ExitCode))." }
        if ($Process.MainWindowHandle -ne [IntPtr]::Zero) {
            $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $($Process.Id) AND Name = 'aether.exe'" |
                Where-Object { $_.ExecutablePath -ieq $Cli })
            if ($children.Count -eq 1) { return $children[0].ProcessId }
        }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'The desktop did not open a main window with its CLI sidecar.'
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $assets = Join-Path $root 'assets'
    New-Item -ItemType Directory -Path $assets | Out-Null
    $newAsset = Join-Path $assets 'aether-windows-amd64.exe'
    Copy-Item -LiteralPath $NewCli -Destination $newAsset
    $newZip = Join-Path $assets 'aether-desktop-windows-amd64.zip'
    Copy-Item -LiteralPath $DesktopZip -Destination $newZip
    Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
    $archive = [IO.Compression.ZipFile]::Open($newZip, 'Update')
    try {
        $entry = $archive.CreateEntry('new-release-marker.txt')
        $writer = New-Object IO.StreamWriter($entry.Open())
        $writer.Write($NewVersion)
        $writer.Dispose()
    } finally { $archive.Dispose() }
    $oldHash = Get-Hash $OldCli
    $newHash = Get-Hash $NewCli
    Assert ($oldHash -ne $newHash) 'the old and new builds differ'

    # The updater pins https://github.com. fake-release answers CONNECT
    # github.com:443 for it (HTTPS_PROXY) with a certificate from a throwaway
    # CA, which only this machine trusts, in LocalMachine\Root.
    $fakeLog = Join-Path $root 'fake-release.out'
    $fake = Start-Process -FilePath $FakeRelease -PassThru -WindowStyle Hidden `
        -ArgumentList '-assets', ('"' + $assets + '"'), '-latest', $NewVersion, '-ca-out', ('"' + (Join-Path $root 'ca.pem') + '"') `
        -RedirectStandardOutput $fakeLog -RedirectStandardError "$fakeLog.err"
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($fake.HasExited) { throw "fake-release exited: $(Get-Content -Raw -LiteralPath "$fakeLog.err")" }
        $proxy = Get-Content -LiteralPath $fakeLog -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($proxy) { break }
        if ([DateTime]::UtcNow -gt $deadline) { throw 'fake-release did not start.' }
        Start-Sleep -Milliseconds 100
    } while ($true)
    & certutil.exe -addstore -f Root (Join-Path $root 'ca.pem')
    if ($LASTEXITCODE -ne 0) { throw 'Could not trust the fake release CA.' }
    $env:HTTPS_PROXY = "http://$proxy"
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
    Assert ((Get-Content -LiteralPath $sentinel) -eq 'preserve this file') 'an unrelated file in the install directory is untouched'

    $updated = Invoke-Native $installed @('update')
    Assert ($updated.Code -eq 0) 'aether update succeeds against the fake release'
    Assert ((Get-Hash $installed) -eq $newHash) 'aether.exe is the new build'
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
    Assert (-not (Test-Path -LiteralPath $old)) 'the next start removed aether.exe.old'
    Assert ((Get-Hash $installed) -ne $oldHash) 'aether.exe is still the new build after the cleanup'
    Assert-NoNewDetections 'after the CLI update'

    Write-Host '=== desktop update through the running app ==='
    $installed = New-Sandbox 'desktop'
    $desktop = Join-Path $env:LOCALAPPDATA 'Programs\Aether Desktop\aether-desktop.exe'
    $install = Invoke-Native $installed @('gui', 'build', '--zip', ('"' + $DesktopZip + '"')) 600
    Assert ($install.Code -eq 0 -and (Test-Path -LiteralPath $desktop)) 'the old desktop app is installed'
    $appDir = Split-Path -Parent $desktop
    Assert (-not (Test-Path -LiteralPath (Join-Path $appDir 'new-release-marker.txt'))) 'the installed app is the old one'

    & schtasks.exe /Create /TN $taskName /TR 'cmd.exe /c ping -n 600 127.0.0.1' /SC ONCE /ST 23:59 /F
    $taskReady = $LASTEXITCODE -eq 0
    if ($taskReady) { & schtasks.exe /Run /TN $taskName; $taskReady = $LASTEXITCODE -eq 0 }
    if ($taskReady) {
        Start-Sleep -Seconds 3
        $taskReady = ((& schtasks.exe /Query /TN $taskName /FO LIST) -join "`n") -match 'Status:\s+Running'
    }
    Write-Host "The stand-in aether-daemon task is running: $taskReady"

    $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $debugPort = $listener.LocalEndpoint.Port
    $listener.Stop()
    $app = Start-Process -FilePath $desktop -PassThru -ArgumentList "--remote-debugging-port=$debugPort", ('--user-data-dir="' + (Join-Path $root 'Electron') + '"')
    $appId = $app.Id
    $sidecarId = Wait-DesktopSidecar $app $installed
    Write-Host "The old desktop (pid $appId) runs its sidecar (pid $sidecarId) from the old CLI."

    $apply = Join-Path $root 'apply.cjs'
    @'
const { chromium } = require(process.argv[2])
;(async () => {
  const browser = await chromium.connectOverCDP(process.argv[3])
  const context = browser.contexts()[0]
  const page = context.pages()[0] || await context.waitForEvent('page')
  await page.waitForURL(/^http:\/\/127\.0\.0\.1:\d+\//, { timeout: 45000 })
  await page.waitForFunction(() => window.sessionStorage.getItem('aether.token'), null, { timeout: 45000 })
  const answer = await page.evaluate(async () => {
    const response = await fetch('/local/v1/update.apply', {
      method: 'POST',
      headers: { authorization: 'Bearer ' + window.sessionStorage.getItem('aether.token'), 'content-type': 'application/json' },
      body: '{}',
    })
    return { status: response.status, body: await response.text() }
  })
  console.log(JSON.stringify(answer))
  if (answer.status !== 200) process.exitCode = 1
  await browser.close().catch(() => {})
})().catch(error => { console.error(error); process.exitCode = 1 })
'@ | Set-Content -LiteralPath $apply -Encoding UTF8
    $applied = Invoke-Native $node @(('"' + $apply + '"'), ('"' + $Playwright + '"'), "http://127.0.0.1:$debugPort") 300
    Assert ($applied.Code -eq 0) 'update.apply answered 200 through the running app'

    $deadline = [DateTime]::UtcNow.AddSeconds(240)
    $relaunched = $null
    do {
        $relaunched = @(Get-ProcessAt 'aether-desktop' $desktop | Where-Object { $_.Id -ne $appId -and $_.MainWindowHandle -ne [IntPtr]::Zero })
        if ($relaunched.Count -gt 0) { break }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($relaunched.Count -eq 0) {
        Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Filter last-error.txt -ErrorAction SilentlyContinue |
            ForEach-Object { Write-Host "$($_.FullName): $(Get-Content -Raw -LiteralPath $_.FullName)" }
        Get-Process -Name 'aether', 'aether-desktop' -ErrorAction SilentlyContinue | Format-Table Id, Path -AutoSize | Out-String | Write-Host
        throw 'The updated desktop app was not started within 240s.'
    }
    Write-Host "ok: a new desktop process (pid $($relaunched[0].Id)) was started from the app directory"
    Assert ((Get-Process -Id $appId -ErrorAction SilentlyContinue) -eq $null) 'the old desktop process exited'
    Assert ((Get-Process -Id $sidecarId -ErrorAction SilentlyContinue) -eq $null) 'the old sidecar exited'
    Assert ((Get-Content -Raw -LiteralPath (Join-Path $appDir 'new-release-marker.txt')).Trim() -eq $NewVersion) 'the app directory holds the new release'
    Assert ((Get-Hash $installed) -eq $newHash) 'the CLI the app runs is the new build'
    $errors = @(Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Filter last-error.txt -ErrorAction SilentlyContinue)
    Assert ($errors.Count -eq 0) 'no desktop build error was recorded'
    Assert (-not (Get-ChildItem -Recurse -Force -LiteralPath $env:LOCALAPPDATA -Directory -Filter staged-app -ErrorAction SilentlyContinue)) 'the staged app was consumed'
    $newSidecar = Wait-DesktopSidecar $relaunched[0] $installed
    Write-Host "ok: the relaunched desktop runs a new sidecar (pid $newSidecar)"
    if ($taskReady) {
        $status = (& schtasks.exe /Query /TN $taskName /FO LIST) -join "`n"
        Assert ($status -notmatch 'Status:\s+Running') 'the helper ended the aether-daemon task'
    } else {
        Write-Host 'NOT VERIFIED: the aether-daemon stand-in task could not be started on this runner.'
    }
    Assert-NoNewDetections 'after the desktop update'

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
    throw
} finally {
    try {
        foreach ($process in @(Get-Process -Name 'aether-desktop', 'aether' -ErrorAction SilentlyContinue |
                Where-Object { $_.Path -and $_.Path -like "$root*" })) {
            & "$env:SystemRoot\System32\taskkill.exe" /PID $process.Id /T /F | Out-Null
        }
        if ($fake -and -not $fake.HasExited) { $fake.Kill() }
        & schtasks.exe /Delete /TN $taskName /F 2>&1 | Out-Null
        & certutil.exe -delstore Root $caSubject 2>&1 | Out-Null
        foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    } catch {
        Write-Host "cleanup: $($_.Exception.Message)"
    }
}
