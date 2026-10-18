# Names the threat behind a Defender failure: the detections and threats it
# recorded and its operational log's detection events (1116 detected, 1117
# remediated). Called from the Windows smoke scripts' failure paths, so it
# never throws.
$ErrorActionPreference = 'Continue'
Write-Host '--- Defender diagnostics ---'
try {
    Get-MpThreatDetection | Select-Object ThreatID, InitialDetectionTime, ProcessName, Resources |
        Format-List | Out-String -Width 300 | Write-Host
    Get-MpThreat | Select-Object ThreatID, ThreatName, @{ n = 'Resources'; e = { $_.Resources -join '; ' } }, InitialDetectionTime |
        Format-List | Out-String -Width 300 | Write-Host
} catch {
    Write-Host "Could not read Defender threats: $_"
}
try {
    Get-WinEvent -FilterHashtable @{ LogName = 'Microsoft-Windows-Windows Defender/Operational'; Id = 1116, 1117 } -MaxEvents 20 -ErrorAction Stop |
        ForEach-Object { "$($_.TimeCreated.ToString('o')) event $($_.Id): $($_.Message)" } | Write-Host
} catch {
    Write-Host "No Defender 1116/1117 events: $_"
}
