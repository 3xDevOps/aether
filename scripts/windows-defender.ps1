# Disposable GitHub-hosted runners only: Prepare removes inherited antivirus
# exclusions before artifacts arrive. Scan uses that baseline to reject every
# new detection, including threats Defender has already remediated.
# StartUpdate runs Update in the background so a job can overlap the signature
# download with other work; the next Prepare waits for it instead of updating.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('StartUpdate', 'Update', 'Prepare', 'Scan')]
    [string]$Mode,
    [string]$ArtifactDirectory = 'dist',
    # Scan only: the file names in ArtifactDirectory that must all be present.
    [string[]]$Artifacts
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This script changes Defender preferences and requires a disposable GitHub-hosted runner.'
}

$statePath = Join-Path $env:RUNNER_TEMP 'aether-defender-state.json'
$updatePath = Join-Path $env:RUNNER_TEMP 'aether-defender-update'
$deadline = (Get-Date).AddMinutes(5)
$mpcmdrun = "$env:ProgramFiles\Windows Defender\MpCmdRun.exe"
$scanningFlags = @(
    'DisableRealtimeMonitoring', 'DisableIOAVProtection',
    'DisableBehaviorMonitoring', 'DisableScriptScanning',
    'DisableArchiveScanning', 'DisableBlockAtFirstSeen'
)

function Write-Evidence([string]$Text) {
    Write-Host $Text
    if ($env:GITHUB_STEP_SUMMARY) {
        Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY -Value $Text -Encoding UTF8
    }
}

function Assert-Protection {
    $status = Get-MpComputerStatus
    $preferences = Get-MpPreference
    $evidence = [ordered]@{
        AMServiceEnabled = $status.AMServiceEnabled
        AntivirusEnabled = $status.AntivirusEnabled
        RealTimeProtectionEnabled = $status.RealTimeProtectionEnabled
        AntivirusSignatureVersion = $status.AntivirusSignatureVersion
        AntivirusSignatureLastUpdated = $status.AntivirusSignatureLastUpdated
        AMEngineVersion = $status.AMEngineVersion
        MAPSReporting = $preferences.MAPSReporting
        SubmitSamplesConsent = $preferences.SubmitSamplesConsent
        ExclusionPath = $preferences.ExclusionPath
        ExclusionProcess = $preferences.ExclusionProcess
        ExclusionExtension = $preferences.ExclusionExtension
    }
    foreach ($flag in $scanningFlags) {
        $evidence[$flag] = $preferences.$flag
    }
    Write-Evidence ('```json' + "`n" + ($evidence | ConvertTo-Json -Depth 4) + "`n" + '```')
    if (-not $status.AMServiceEnabled -or -not $status.AntivirusEnabled -or -not $status.RealTimeProtectionEnabled) {
        throw 'Defender antivirus and real-time protection must actually be running.'
    }
    foreach ($flag in $scanningFlags) {
        if ($preferences.$flag -ne $false) {
            throw "Defender protection is not enabled: $flag=$($preferences.$flag)"
        }
    }
    if ($preferences.MAPSReporting -ne 2 -or $preferences.SubmitSamplesConsent -ne 1) {
        throw 'Defender cloud reporting and automatic safe sample submission did not take effect.'
    }
    if ($preferences.ExclusionPath -or $preferences.ExclusionProcess -or $preferences.ExclusionExtension) {
        throw 'Inherited Defender antivirus exclusions remain active.'
    }
}

# Fresh runners update the Defender platform shortly after boot, which
# restarts WinDefend. Meanwhile cmdlets fail with 0x800106ba (RPC server
# unavailable), MpCmdRun with 0x80070652 (another update is installing), and
# real-time protection reports disabled for a while after it is enabled.
function Wait-Defender([switch]$RealTime) {
    while ($true) {
        $service = Get-Service -Name WinDefend
        if ($service.StartType -eq 'Disabled') {
            throw 'The WinDefend service is disabled on this runner image.'
        }
        try {
            if ($service.Status -eq 'Stopped') { Start-Service -Name WinDefend }
            $status = Get-MpComputerStatus
            $state = "AMServiceEnabled=$($status.AMServiceEnabled) AntivirusEnabled=$($status.AntivirusEnabled) RealTimeProtectionEnabled=$($status.RealTimeProtectionEnabled)"
            if ($status.AMServiceEnabled -and $status.AntivirusEnabled -and (-not $RealTime -or $status.RealTimeProtectionEnabled)) {
                return
            }
        } catch {
            $state = "$_"
            Write-Host "Defender is not answering yet; retrying: $state"
        }
        if ((Get-Date) -gt $deadline) {
            throw "Defender was not ready by $deadline. WinDefend: $($service.Status). Last status: $state"
        }
        Start-Sleep -Seconds 5
    }
}

function Invoke-Defender([string]$Action, [scriptblock]$Command) {
    while ($true) {
        Wait-Defender
        try {
            & $Command
            return
        } catch {
            if ("$_" -notmatch '0x800106ba|0x80070652' -or (Get-Date) -gt $deadline) {
                throw "${Action} failed: $_"
            }
            Write-Host "${Action} hit a Defender restart or concurrent update; retrying: $_"
        }
        Start-Sleep -Seconds 10
    }
}

function Update-Signatures {
    Invoke-Defender 'Defender signature update' {
        $output = (& $mpcmdrun -SignatureUpdate) -join "`n"
        Write-Host $output
        if ($LASTEXITCODE -ne 0) { throw "MpCmdRun exit ${LASTEXITCODE}: $output" }
    }
}

if ($Mode -eq 'StartUpdate') {
    # Without redirection Start-Process uses ShellExecute, so the child holds
    # no copy of this step's output pipes and the runner does not wait for it.
    $process = Start-Process -FilePath powershell.exe -PassThru -WindowStyle Hidden `
        -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`"", '-Mode', 'Update')
    Set-Content -LiteralPath "$updatePath.pid" -Value "$($process.Id) $($process.StartTime.ToFileTimeUtc())"
    Write-Host "Defender signature update running in the background as process $($process.Id)."
    return
}
if ($Mode -eq 'Update') {
    try {
        Update-Signatures *> "$updatePath.log"
        Set-Content -LiteralPath "$updatePath.ok" -Value $env:GITHUB_RUN_ID
    } catch {
        Add-Content -LiteralPath "$updatePath.log" -Value "$_"
        throw
    }
    return
}

Write-Evidence "## Windows Defender: $Mode"
if ($Mode -eq 'Prepare') {
    # Record before arming or downloading, so real-time detections during the
    # download cannot become part of a later, apparently clean baseline.
    $baseline = @(Invoke-Defender 'Reading the detection baseline' {
        Get-MpThreatDetection | ForEach-Object { [string]$_.DetectionID }
    })
    Invoke-Defender 'Arming Defender' {
        Set-MpPreference -DisableRealtimeMonitoring $false
        Set-MpPreference -DisableIOAVProtection $false
        Set-MpPreference -DisableBehaviorMonitoring $false
        Set-MpPreference -DisableScriptScanning $false
        Set-MpPreference -DisableArchiveScanning $false
        Set-MpPreference -DisableBlockAtFirstSeen $false
        Set-MpPreference -MAPSReporting 2
        Set-MpPreference -SubmitSamplesConsent 1
        $preferences = Get-MpPreference
        if ($preferences.ExclusionPath) { Remove-MpPreference -ExclusionPath $preferences.ExclusionPath }
        if ($preferences.ExclusionProcess) { Remove-MpPreference -ExclusionProcess $preferences.ExclusionProcess }
        if ($preferences.ExclusionExtension) { Remove-MpPreference -ExclusionExtension $preferences.ExclusionExtension }
    }
    if (Test-Path -LiteralPath "$updatePath.pid") {
        # Windows reuses process IDs quickly, so match the start time as well.
        $updateId, $updateStart = (Get-Content -LiteralPath "$updatePath.pid" -Raw).Trim() -split ' '
        try {
            Get-Process -Id $updateId -ErrorAction SilentlyContinue |
                Where-Object { $_.StartTime.ToFileTimeUtc() -eq [long]$updateStart } |
                Wait-Process -Timeout ([math]::Max(1, [int]($deadline - (Get-Date)).TotalSeconds))
        } finally {
            Write-Host 'Background signature update output:'
            Get-Content -LiteralPath "$updatePath.log" -ErrorAction Continue | ForEach-Object { Write-Host $_ }
        }
        if (-not (Test-Path -LiteralPath "$updatePath.ok")) {
            throw 'The background Defender signature update failed; its output is above.'
        }
    } else {
        Update-Signatures
    }
    Wait-Defender -RealTime
    Invoke-Defender 'Verifying Defender protection' { Assert-Protection }
    [ordered]@{
        RunID = $env:GITHUB_RUN_ID
        RunAttempt = $env:GITHUB_RUN_ATTEMPT
        DetectionIDs = $baseline
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $statePath -Encoding UTF8
    Write-Evidence "Protection verified; signature update succeeded; baseline contains $($baseline.Count) detection(s)."
    return
}

if (-not $Artifacts) { throw 'Scan needs -Artifacts: the file names in -ArtifactDirectory to scan.' }
$state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
if ($state.RunID -ne $env:GITHUB_RUN_ID -or $state.RunAttempt -ne $env:GITHUB_RUN_ATTEMPT) {
    throw 'Defender baseline is not from this workflow run and attempt.'
}
Assert-Protection
$failures = @()
# Every file whose bytes must survive the scan, mapped to its SHA256 before
# it: each artifact, and every file a zip artifact holds. A zip is scanned
# both whole and extracted, as a user's machine meets it.
$hashes = [ordered]@{}
$scans = [ordered]@{}
$extracted = Join-Path $env:RUNNER_TEMP 'aether-defender-extracted'
Add-Type -AssemblyName System.IO.Compression.FileSystem
Write-Evidence '| Artifact | SHA256 before scan |'
Write-Evidence '| --- | --- |'
foreach ($name in $Artifacts) {
    try {
        $file = Get-Item -LiteralPath (Join-Path $ArtifactDirectory $name)
        if ($file -isnot [System.IO.FileInfo] -or $file.Length -eq 0) {
            throw 'Expected a nonempty file.'
        }
        # Defender's service cannot resolve paths relative to this shell.
        $scans[$name] = $file.FullName
        $hashes[$file.FullName] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
        Write-Evidence "| $name | $($hashes[$file.FullName]) |"
        if ($file.Extension -eq '.zip') {
            $dir = Join-Path $extracted $file.BaseName
            [System.IO.Compression.ZipFile]::ExtractToDirectory($file.FullName, $dir)
            $contents = @(Get-ChildItem -LiteralPath $dir -File -Recurse)
            foreach ($entry in $contents) {
                $hashes[$entry.FullName] = (Get-FileHash -LiteralPath $entry.FullName -Algorithm SHA256).Hash
            }
            $scans["$name (extracted, $($contents.Count) files)"] = $dir
        }
    } catch {
        $failures += "${name}: missing, quarantined, or unreadable before scanning: $_"
    }
}
foreach ($label in $scans.Keys) {
    $path = $scans[$label]
    try {
        Write-Host "Scanning $path with remediation enabled"
        & $mpcmdrun -Scan -ScanType 3 -File $path
        $code = $LASTEXITCODE
        Write-Evidence "${label}: MpCmdRun exit $code."
        if ($code -ne 0) { $failures += "${label}: MpCmdRun exit $code" }
    } catch {
        $failures += "${label}: scan failed: $_"
    }
}
# A successful native exit code can mean that remediation succeeded. It does
# not clear a detection, and every file must still match its original bytes.
foreach ($path in $hashes.Keys) {
    try {
        $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
        if ($scans.Values -contains $path) { Write-Evidence "$(Split-Path -Leaf $path): SHA256 after scan $hash." }
        if ($hash -ne $hashes[$path]) {
            $failures += "${path}: SHA256 changed during scanning"
        }
    } catch {
        $failures += "${path}: missing, quarantined, or unreadable after scanning: $_"
    }
}
$newDetections = @(Get-MpThreatDetection | Where-Object { [string]$_.DetectionID -notin @($state.DetectionIDs) })
if ($newDetections.Count -gt 0) {
    Write-Evidence ('```json' + "`n" + ($newDetections | ConvertTo-Json -Depth 6) + "`n" + '```')
    $failures += "Defender recorded $($newDetections.Count) new detection(s), including any already remediated."
}
if ($failures.Count -gt 0) {
    $logPath = Join-Path ([System.IO.Path]::GetTempPath()) 'MpCmdRun.log'
    Write-Host "MpCmdRun diagnostics: $logPath (last 200 lines)"
    try {
        Get-Content -LiteralPath $logPath -Tail 200 | ForEach-Object { Write-Host $_ }
    } catch {
        Write-Host "Could not read MpCmdRun diagnostics at ${logPath}: $_"
    }
}
Assert-Protection
if ($failures.Count -gt 0) {
    foreach ($failure in $failures) { Write-Evidence "FAIL: $failure" }
    throw 'Windows Defender validation failed; see the evidence above.'
}
Write-Evidence "All $($Artifacts.Count) artifacts passed: unchanged SHA256, no new detections, and effective protection still enabled."
