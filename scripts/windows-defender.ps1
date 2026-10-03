# Disposable GitHub-hosted runners only: Prepare removes inherited antivirus
# exclusions before artifacts arrive. Scan uses that baseline to reject every
# new detection, including threats Defender has already remediated.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Prepare', 'Scan')]
    [string]$Mode,
    [string]$ArtifactDirectory = 'dist'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This script changes Defender preferences and requires a disposable GitHub-hosted runner.'
}

$statePath = Join-Path $env:RUNNER_TEMP 'aether-defender-state.json'
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

Write-Evidence "## Windows Defender: $Mode"
if ($Mode -eq 'Prepare') {
    # Record before arming or downloading, so real-time detections during the
    # download cannot become part of a later, apparently clean baseline.
    $baseline = @(Get-MpThreatDetection | ForEach-Object { [string]$_.DetectionID })
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
    & $mpcmdrun -SignatureUpdate
    if ($LASTEXITCODE -ne 0) {
        throw "Defender signature update failed (exit $LASTEXITCODE)."
    }
    Assert-Protection
    [ordered]@{
        RunID = $env:GITHUB_RUN_ID
        RunAttempt = $env:GITHUB_RUN_ATTEMPT
        DetectionIDs = $baseline
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $statePath -Encoding UTF8
    Write-Evidence "Protection verified; signature update succeeded; baseline contains $($baseline.Count) detection(s)."
    return
}

$state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
if ($state.RunID -ne $env:GITHUB_RUN_ID -or $state.RunAttempt -ne $env:GITHUB_RUN_ATTEMPT) {
    throw 'Defender baseline is not from this workflow run and attempt.'
}
Assert-Protection
$failures = @()
$hashes = [ordered]@{}
$paths = [ordered]@{}
$names = @('aether-windows-amd64.exe', 'aether-windows-arm64.exe')
Write-Evidence '| Binary | SHA256 before scan |'
Write-Evidence '| --- | --- |'
foreach ($name in $names) {
    $path = Join-Path $ArtifactDirectory $name
    try {
        $file = Get-Item -LiteralPath $path
        if ($file -isnot [System.IO.FileInfo] -or $file.Length -eq 0) {
            throw 'Expected a nonempty executable file.'
        }
        # Defender's service cannot resolve paths relative to this shell.
        $paths[$name] = $file.FullName
        $hashes[$name] = (Get-FileHash -LiteralPath $paths[$name] -Algorithm SHA256).Hash
        Write-Evidence "| $name | $($hashes[$name]) |"
    } catch {
        $failures += "${name}: missing, quarantined, or unreadable before scanning: $_"
    }
}
foreach ($name in $names) {
    if (-not $hashes.Contains($name)) { continue }
    $path = $paths[$name]
    try {
        Write-Host "Scanning $path with remediation enabled"
        & $mpcmdrun -Scan -ScanType 3 -File $path
        $code = $LASTEXITCODE
        Write-Evidence "${name}: MpCmdRun exit $code."
        if ($code -ne 0) { $failures += "${name}: MpCmdRun exit $code" }
    } catch {
        $failures += "${name}: scan failed: $_"
    }
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
# A successful native exit code can mean that remediation succeeded. It does
# not clear a detection, and both files must still match their original bytes.
foreach ($name in $names) {
    try {
        if (-not $paths.Contains($name)) {
            throw 'No executable file was resolved before scanning.'
        }
        $hash = (Get-FileHash -LiteralPath $paths[$name] -Algorithm SHA256).Hash
        Write-Evidence "${name}: SHA256 after scan $hash."
        if (-not $hashes.Contains($name) -or $hash -ne $hashes[$name]) {
            $failures += "${name}: SHA256 changed during scanning"
        }
    } catch {
        $failures += "${name}: missing, quarantined, or unreadable after scanning: $_"
    }
}
$newDetections = @(Get-MpThreatDetection | Where-Object { [string]$_.DetectionID -notin @($state.DetectionIDs) })
if ($newDetections.Count -gt 0) {
    Write-Evidence ('```json' + "`n" + ($newDetections | ConvertTo-Json -Depth 6) + "`n" + '```')
    $failures += "Defender recorded $($newDetections.Count) new detection(s), including any already remediated."
}
Assert-Protection
if ($failures.Count -gt 0) {
    foreach ($failure in $failures) { Write-Evidence "FAIL: $failure" }
    throw 'Windows Defender validation failed; see the evidence above.'
}
Write-Evidence 'Both Windows architectures passed: unchanged SHA256, no new detections, and effective protection still enabled.'
