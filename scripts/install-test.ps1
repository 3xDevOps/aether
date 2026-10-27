<#
.SYNOPSIS
  Runs the standalone regression suite for scripts/install.ps1.

.DESCRIPTION
  This suite has no Pester or external test-framework dependency.  It compiles
  one tiny Go fixture (the fixture is never executed by the test harness
  itself), serves controlled releases, a latest-release redirect, and a GitHub
  releases API response from a local .NET HttpListener, and
  launches the real installer in a separate PowerShell process.  It snapshots
  and restores the current user's PATH and process environment in finally.

  Invoke from either supported PowerShell runtime:
    powershell -NoProfile -File scripts/install-test.ps1
    pwsh -NoProfile -File scripts/install-test.ps1
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Assert-AetherTest {
    param([bool] $Condition, [string] $Message)
    if (-not $Condition) {
        throw ('install-test: ' + $Message)
    }
}

function Assert-AetherEqual {
    param($Expected, $Actual, [string] $Message)
    if ($Expected -cne $Actual) {
        throw ('install-test: {0}; expected [{1}], got [{2}]' -f $Message, $Expected, $Actual)
    }
}

function Get-AetherTestBytesEqual {
    param([string] $Left, [string] $Right)
    if (-not (Test-Path -LiteralPath $Left) -or -not (Test-Path -LiteralPath $Right)) {
        return $false
    }
    return (Get-FileHash -LiteralPath $Left -Algorithm SHA256).Hash -ceq
        (Get-FileHash -LiteralPath $Right -Algorithm SHA256).Hash
}

function Quote-AetherProcessArgument {
    param([Parameter(Mandatory = $true)][string] $Value)
    if ($Value -notmatch '[\s"]') { return $Value }
    $escaped = $Value.Replace('"', '\"')
    $trailing = 0
    for ($i = $Value.Length - 1; $i -ge 0 -and $Value[$i] -eq '\'; $i--) {
        $trailing++
    }
    if ($trailing -gt 0) {
        $escaped += ('\' * $trailing)
    }
    return '"' + $escaped + '"'
}

function Get-AetherPowerShellExecutable {
    $candidate = if ($PSVersionTable.PSEdition -eq 'Desktop') {
        Join-Path $PSHOME 'powershell.exe'
    }
    else {
        Join-Path $PSHOME 'pwsh.exe'
    }
    Assert-AetherTest (Test-Path -LiteralPath $candidate -PathType Leaf) ("cannot find the current PowerShell executable: {0}" -f $candidate)
    return $candidate
}

function Invoke-AetherPowerShellFile {
    param(
        [Parameter(Mandatory = $true)][string] $ScriptPath,
        [string[]] $Arguments = @(),
        [hashtable] $Environment = @{}
    )
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = Get-AetherPowerShellExecutable
    $start.WorkingDirectory = $root
    $argumentText = @('-NoProfile', '-NonInteractive', '-File', (Quote-AetherProcessArgument $ScriptPath))
    foreach ($argument in $Arguments) {
        $argumentText += (Quote-AetherProcessArgument ([string]$argument))
    }
    $start.Arguments = ($argumentText -join ' ')
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($name in @('AETHER_VERSION', 'AETHER_BIN_DIR', 'AETHER_ROLE', 'AETHER_REPO', 'AETHER_BASE_URL', 'AETHER_LATEST_URL', 'AETHER_API_URL', 'AETHER_BIN', 'AETHER_FIXTURE_LOG', 'AETHER_DESKTOP_TARGET', 'AETHER_GUI_FAIL', 'PROCESSOR_ARCHITEW6432', 'PROCESSOR_ARCHITECTURE', 'LOCALAPPDATA', 'CURL_HOME')) {
        $start.EnvironmentVariables.Remove($name)
    }
    foreach ($name in $Environment.Keys) {
        $value = $Environment[$name]
        if ($null -eq $value) {
            $start.EnvironmentVariables.Remove([string]$name)
        }
        else {
            $start.EnvironmentVariables[[string]$name] = [string]$value
        }
    }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $start
    Assert-AetherTest $process.Start() ("could not start {0}" -f $start.FileName)
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(60000)) {
        $process.Kill()
        throw 'install-test: installer process timed out'
    }
    $stdout = $stdoutTask.Result
    $stderr = $stderrTask.Result
    return [pscustomobject]@{
        ExitCode = $process.ExitCode
        Stdout = $stdout
        Stderr = $stderr
        Output = $stdout + $stderr
    }
}

function Start-AetherFixtureServer {
    param(
        [Parameter(Mandatory = $true)][string] $Root,
        [Parameter(Mandatory = $true)][string] $ReadyPath,
        [Parameter(Mandatory = $true)][string] $StopPath,
        [Parameter(Mandatory = $true)][string] $RequestLog
    )
    $tcp = New-Object System.Net.Sockets.TcpListener -ArgumentList @([Net.IPAddress]::Loopback, 0)
    $tcp.Start()
    $port = ([Net.IPEndPoint]$tcp.LocalEndpoint).Port
    $tcp.Stop()
    $job = Start-Job -ArgumentList $Root, $port, $ReadyPath, $StopPath, $RequestLog -ScriptBlock {
        param($Root, $Port, $ReadyPath, $StopPath, $RequestLog)
        $listener = New-Object System.Net.HttpListener
        $listener.Prefixes.Add(('http://127.0.0.1:{0}/' -f $Port))
        try {
            $listener.Start()
            [IO.File]::WriteAllText($ReadyPath, 'ready')
            while (-not (Test-Path -LiteralPath $StopPath)) {
                $pending = $listener.GetContextAsync()
                while (-not $pending.Wait(100)) {
                    if (Test-Path -LiteralPath $StopPath) { break }
                }
                if (Test-Path -LiteralPath $StopPath) { break }
                $context = $pending.Result
                $relative = $context.Request.Url.AbsolutePath.Trim('/')
                Add-Content -LiteralPath $RequestLog -Value $relative
                $response = $context.Response
                $agentFile = Join-Path $Root 'only-agent.txt'
                if ((Test-Path -LiteralPath $agentFile -PathType Leaf) -and
                    -not ([string]$context.Request.UserAgent).StartsWith([IO.File]::ReadAllText($agentFile))) {
                    $response.Abort()
                    continue
                }
                $response.StatusCode = 404
                $body = $null
                $latestFile = Join-Path $Root 'latest.txt'
                if ($relative -eq 'latest' -and (Test-Path -LiteralPath $latestFile -PathType Leaf)) {
                    $response.StatusCode = 302
                    $response.RedirectLocation = ('http://127.0.0.1:{0}/tag/{1}' -f $Port, [IO.File]::ReadAllText($latestFile).Trim())
                }
                elseif ($relative.StartsWith('tag/')) {
                    $response.StatusCode = 200
                    $body = [Text.Encoding]::UTF8.GetBytes('release page')
                }
                elseif ($relative -eq 'api') {
                    $response.StatusCode = [int][IO.File]::ReadAllText((Join-Path $Root 'api-status.txt'))
                    $rateLimitFile = Join-Path $Root 'api-ratelimit.txt'
                    if (Test-Path -LiteralPath $rateLimitFile -PathType Leaf) {
                        $response.AddHeader('X-RateLimit-Remaining', [IO.File]::ReadAllText($rateLimitFile).Trim())
                    }
                    $response.ContentType = 'application/json'
                    $body = [IO.File]::ReadAllBytes((Join-Path $Root 'api.json'))
                }
                elseif ($relative -match '^[0-9A-Za-z][0-9A-Za-z.+-]*/[0-9A-Za-z][0-9A-Za-z.+-]*$') {
                    $file = Join-Path $Root $relative
                    if (Test-Path -LiteralPath $file -PathType Leaf) {
                        $response.StatusCode = 200
                        $body = [IO.File]::ReadAllBytes($file)
                    }
                }
                if ($null -ne $body -and $context.Request.HttpMethod -ne 'HEAD') {
                    $response.ContentLength64 = $body.Length
                    $response.OutputStream.Write($body, 0, $body.Length)
                }
                $context.Response.Close()
            }
        }
        finally {
            if ($listener.IsListening) { $listener.Stop() }
            $listener.Close()
        }
    }
    for ($i = 0; $i -lt 100; $i++) {
        if (Test-Path -LiteralPath $ReadyPath) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not (Test-Path -LiteralPath $ReadyPath)) {
        try { Stop-Job -Job $job -ErrorAction SilentlyContinue } catch {}
        try { Remove-Job -Job $job -Force -ErrorAction SilentlyContinue } catch {}
        throw 'install-test: local release server did not start'
    }
    return [pscustomobject]@{ Job = $job; BaseUrl = ('http://127.0.0.1:{0}' -f $port) }
}

function Stop-AetherFixtureServer {
    param($Server, [string] $StopPath)
    if ($null -eq $Server) { return }
    try { [IO.File]::WriteAllText($StopPath, 'stop') } catch {}
    try { Wait-Job -Job $Server.Job -Timeout 5 | Out-Null } catch {}
    try { Stop-Job -Job $Server.Job -ErrorAction SilentlyContinue } catch {}
    try { Remove-Job -Job $Server.Job -Force -ErrorAction SilentlyContinue } catch {}
}

function Write-AetherPayload {
    param([Parameter(Mandatory = $true)][string] $Path, [Parameter(Mandatory = $true)][string] $Payload)
    if (Test-Path -LiteralPath $Payload -PathType Leaf) {
        Copy-Item -LiteralPath $Payload -Destination $Path -Force
    }
    else {
        [IO.File]::WriteAllText($Path, $Payload)
    }
}

function Write-AetherRelease {
    param(
        [Parameter(Mandatory = $true)][string] $ReleaseRoot,
        [Parameter(Mandatory = $true)][string] $Tag,
        [Parameter(Mandatory = $true)][string] $AmdPayload,
        [Parameter(Mandatory = $true)][string] $ArmPayload,
        [bool] $BadAmdChecksum = $false,
        [string] $ManifestVersion = $Tag,
        [string[]] $Omit = @()
    )
    $dir = Join-Path $ReleaseRoot $Tag
    if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $amdPath = Join-Path $dir 'aether-windows-amd64.exe'
    $armPath = Join-Path $dir 'aether-windows-arm64.exe'
    Write-AetherPayload $amdPath $AmdPayload
    Write-AetherPayload $armPath $ArmPayload
    $amdHash = (Get-FileHash -LiteralPath $amdPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $armHash = (Get-FileHash -LiteralPath $armPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($BadAmdChecksum) { $amdHash = ('0' * 64) }
    $lines = @(('{0}  aether-windows-amd64.exe' -f $amdHash))
    $lines += ('{0}  aether-windows-arm64.exe' -f $armHash)
    [IO.File]::WriteAllText((Join-Path $dir 'checksums.txt'), (($lines -join "`r`n") + "`r`n"))
    $manifest = [ordered]@{
        schema = 1
        version = $ManifestVersion
        commit = '0000000000000000000000000000000000000000'
        published_at = '2026-01-01T00:00:00Z'
        notes_url = 'https://example.invalid/notes'
        store_schema = 1
        assets = [ordered]@{
            'aether-windows-amd64.exe' = [ordered]@{ sha256 = $amdHash; size = (Get-Item -LiteralPath $amdPath).Length }
            'aether-windows-arm64.exe' = [ordered]@{ sha256 = $armHash; size = (Get-Item -LiteralPath $armPath).Length }
        }
    }
    [IO.File]::WriteAllText((Join-Path $dir 'aether-release.json'), (ConvertTo-Json -InputObject $manifest -Depth 5))
    [IO.File]::WriteAllText((Join-Path $dir 'aether-release.json.sig'), 'dW52ZXJpZmllZA==')
    foreach ($name in $Omit) {
        Remove-Item -LiteralPath (Join-Path $dir $name) -Force
    }
}

function Write-AetherApi {
    param(
        [Parameter(Mandatory = $true)][string] $ReleaseRoot,
        [string[]] $Tags = @(),
        [string[]] $Drafts = @(),
        [string[]] $Prereleases = @(),
        [int] $Status = 200,
        [string] $RateLimitRemaining,
        [string] $Message = 'API rate limit exceeded'
    )
    $entries = @()
    foreach ($tag in $Tags) {
        $dir = Join-Path $ReleaseRoot $tag
        $assets = @()
        if (Test-Path -LiteralPath $dir) {
            $assets = @(Get-ChildItem -LiteralPath $dir -File | ForEach-Object { [ordered]@{ name = $_.Name } })
        }
        $entries += [ordered]@{ tag_name = $tag; draft = ($Drafts -contains $tag); prerelease = ($Prereleases -contains $tag); assets = $assets }
    }
    $body = if ($Status -eq 200) { ConvertTo-Json -InputObject @($entries) -Depth 5 } else { ConvertTo-Json -Compress -InputObject ([ordered]@{ message = $Message }) }
    [IO.File]::WriteAllText((Join-Path $ReleaseRoot 'api.json'), $body)
    [IO.File]::WriteAllText((Join-Path $ReleaseRoot 'api-status.txt'), [string]$Status)
    $rateLimitFile = Join-Path $ReleaseRoot 'api-ratelimit.txt'
    if ([string]::IsNullOrEmpty($RateLimitRemaining)) {
        Remove-Item -LiteralPath $rateLimitFile -Force -ErrorAction SilentlyContinue
    }
    else {
        [IO.File]::WriteAllText($rateLimitFile, $RateLimitRemaining)
    }
}

function Set-AetherUserPath {
    param([AllowNull()][string] $Value)
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    }
    try {
        $kind = if ($null -ne $Value -and $Value.Contains('%')) {
            [Microsoft.Win32.RegistryValueKind]::ExpandString
        }
        else {
            [Microsoft.Win32.RegistryValueKind]::String
        }
        $key.SetValue('Path', $Value, $kind)
    }
    finally {
        $key.Close()
    }
}

function Get-AetherNormalizedPathEntry {
    param([string] $Entry)
    if ($null -eq $Entry) { return '' }
    $value = $Entry.Trim().Trim('"').Replace('/', '\')
    while ($value.Length -gt 3 -and $value.EndsWith('\')) {
        $value = $value.Substring(0, $value.Length - 1)
    }
    return $value.ToLowerInvariant()
}

function Get-AetherTestUserPath {
    return $userEnvironment.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'install-test: this suite must run on Windows'
}

$installer = Join-Path $PSScriptRoot 'install.ps1'
Assert-AetherTest (Test-Path -LiteralPath $installer -PathType Leaf) ("installer not found: {0}" -f $installer)
$goCommand = Get-Command go -ErrorAction SilentlyContinue
Assert-AetherTest ($null -ne $goCommand) 'Go is required to compile the isolated CLI fixture'

$userEnvironment = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if ($null -eq $userEnvironment) {
    $userEnvironment = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
}
$hadUserPath = $userEnvironment.GetValueNames() -contains 'Path'
$originalUserPath = $userEnvironment.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$originalUserPathKind = if ($hadUserPath) { $userEnvironment.GetValueKind('Path') } else { $null }
$root = Join-Path ([IO.Path]::GetTempPath()) ('.aether-install-test-{0}' -f ([Guid]::NewGuid().ToString('N')))
$server = $null
$stopPath = Join-Path $root 'server.stop'
try {
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    $source = Join-Path $root 'fixture.go'
    $fixture = Join-Path $root 'fixture.exe'
    $fixtureSource = @'
package main

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    logPath := os.Getenv("AETHER_FIXTURE_LOG")
    if logPath != "" {
        _ = os.WriteFile(logPath, []byte("invoked"), 0644)
    }
    if strings.Join(os.Args[1:], " ") != "gui build" {
        fmt.Fprintln(os.Stderr, "unsupported fixture command")
        os.Exit(64)
    }
    fmt.Println("fixture stdout progress")
    fmt.Fprintln(os.Stderr, "fixture stderr progress")
    if os.Getenv("AETHER_GUI_FAIL") != "" {
        fmt.Fprintln(os.Stderr, "fixture desktop failure")
        os.Exit(23)
    }
    target := os.Getenv("AETHER_DESKTOP_TARGET")
    if target != "" {
        _ = os.MkdirAll(filepath.Dir(target), 0755)
        _ = os.WriteFile(target, []byte("desktop fixture"), 0644)
    }
}
'@
    [IO.File]::WriteAllText($source, $fixtureSource)
    # Compilation creates the fixture but does not execute it.  The first
    # execution of the fixture is therefore necessarily after installer hash
    # verification, inside the GUI-build path under test.  Build outside the
    # repository module so this temporary one-file package is portable.
    $oldGo111 = [Environment]::GetEnvironmentVariable('GO111MODULE', 'Process')
    $oldGoCache = [Environment]::GetEnvironmentVariable('GOCACHE', 'Process')
    try {
        [Environment]::SetEnvironmentVariable('GO111MODULE', 'off', 'Process')
        [Environment]::SetEnvironmentVariable('GOCACHE', (Join-Path $root 'go-cache'), 'Process')
        Push-Location $root
        & $goCommand.Source build -o $fixture 'fixture.go'
        Assert-AetherEqual 0 $LASTEXITCODE 'Go fixture compilation failed'
    }
    finally {
        Pop-Location
        [Environment]::SetEnvironmentVariable('GO111MODULE', $oldGo111, 'Process')
        [Environment]::SetEnvironmentVariable('GOCACHE', $oldGoCache, 'Process')
    }

    $releaseRoot = Join-Path $root 'release'
    New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
    $readyPath = Join-Path $root 'server.ready'
    $requestLog = Join-Path $root 'requests.log'
    [IO.File]::WriteAllText($requestLog, '')
    $server = Start-AetherFixtureServer $releaseRoot $readyPath $stopPath $requestLog
    $baseUrl = $server.BaseUrl
    $version = 'v0.5.1-alpha.4'

    # A poisoned checksum must not replace an existing executable, append PATH,
    # or execute the downloaded fixture.
    $checksumCase = Join-Path $root 'checksum-case'
    $checksumBin = Join-Path $checksumCase 'bin'
    New-Item -ItemType Directory -Path $checksumBin -Force | Out-Null
    $oldCli = Join-Path $checksumBin 'aether.exe'
    [IO.File]::WriteAllText($oldCli, 'old checksum-case CLI')
    $beforePath = '%SystemRoot%\System32;C:\unrelated-tools'
    Set-AetherUserPath $beforePath
    Write-AetherRelease $releaseRoot $version $fixture $fixture -BadAmdChecksum $true
    $checksumLog = Join-Path $checksumCase 'fixture.log'
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $checksumBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $checksumLog
    }
    Assert-AetherEqual 1 $result.ExitCode 'bad checksum unexpectedly succeeded'
    Assert-AetherTest ($result.Stderr.Contains('The download was deleted and nothing was installed.')) ("bad checksum did not say what happened: " + $result.Output)
    # Under -File the error is printed once, without a PowerShell error record.
    $errorLines = @($result.Stderr -split "`r?`n" | Where-Object { $_ -ne '' })
    Assert-AetherEqual 1 $errorLines.Count ("bad checksum printed more than one error line: " + $result.Stderr)
    Assert-AetherTest ($errorLines[0].StartsWith('aether install: error: checksum mismatch for aether-windows-amd64.exe')) ("bad checksum error line: " + $result.Stderr)
    Assert-AetherEqual 'old checksum-case CLI' ([IO.File]::ReadAllText($oldCli)) 'bad checksum replaced the existing CLI'
    Assert-AetherEqual $beforePath (Get-AetherTestUserPath) 'bad checksum changed user PATH'
    Assert-AetherTest (-not (Test-Path -LiteralPath $checksumLog)) 'bad checksum executed the fixture'
    Write-AetherRelease $releaseRoot $version $fixture $fixture
    $checksumPath = Join-Path (Join-Path $releaseRoot $version) 'checksums.txt'
    $validManifest = [IO.File]::ReadAllText($checksumPath)
    foreach ($manifest in @($validManifest + $validManifest, 'invalid checksum', (('0' * 64) + '  another.exe'))) {
        [IO.File]::WriteAllText($checksumPath, $manifest)
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $checksumBin) @{
            AETHER_BASE_URL = $baseUrl
        }
        Assert-AetherTest ($result.ExitCode -ne 0) 'invalid checksum manifest was accepted'
        Assert-AetherEqual 'old checksum-case CLI' ([IO.File]::ReadAllText($oldCli)) 'invalid manifest replaced the CLI'
        Assert-AetherEqual $beforePath (Get-AetherTestUserPath) 'invalid manifest changed user PATH'
    }

    Write-AetherRelease $releaseRoot $version $fixture $fixture
    $lockedFile = [IO.File]::Open($oldCli, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $checksumBin) @{
            AETHER_BASE_URL = $baseUrl
        }
        Assert-AetherTest ($result.ExitCode -ne 0) 'locked CLI replacement was reported as success'
        Assert-AetherTest ($result.Stderr.Contains('aether install: error: cannot replace ' + $oldCli + ': ')) ("locked CLI did not name the file: " + $result.Output)
        Assert-AetherTest ($result.Stderr.Contains('Nothing was changed.') -and $result.Stderr.Contains("Then rerun: & '" + $installer + "' -Version '" + $version + "'")) ("locked CLI did not give the rerun command: " + $result.Output)
    } finally {
        $lockedFile.Dispose()
    }
    Assert-AetherEqual 'old checksum-case CLI' ([IO.File]::ReadAllText($oldCli)) 'locked CLI was damaged'

    # A running CLI is named with the command that stops it, before any change.
    $runningBin = Join-Path $root 'running-bin'
    New-Item -ItemType Directory -Path $runningBin -Force | Out-Null
    $runningCli = Join-Path $runningBin 'aether.exe'
    Copy-Item -LiteralPath (Join-Path $env:SystemRoot 'System32\PING.EXE') -Destination $runningCli
    $runningProcess = Start-Process -FilePath $runningCli -ArgumentList @('-n', '60', '127.0.0.1') -WindowStyle Hidden -PassThru
    try {
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $runningBin) @{
            AETHER_BASE_URL = $baseUrl
        }
    } finally {
        Stop-Process -Id $runningProcess.Id -Force -ErrorAction SilentlyContinue
        $runningProcess.WaitForExit()
    }
    Assert-AetherTest ($result.ExitCode -ne 0) 'install over a running CLI was reported as success'
    foreach ($expected in @(('aether (pid {0}) {1}' -f $runningProcess.Id, $runningCli), 'Nothing was changed.', ('Stop-Process -Id {0}' -f $runningProcess.Id))) {
        Assert-AetherTest ($result.Stderr.Contains($expected)) ("running CLI message lacks [{0}]: {1}" -f $expected, $result.Output)
    }
    Assert-AetherTest (Get-AetherTestBytesEqual (Join-Path $env:SystemRoot 'System32\PING.EXE') $runningCli) 'install over a running CLI replaced it'
    Assert-AetherEqual $beforePath (Get-AetherTestUserPath) 'locked CLI changed user PATH'

    # A fresh default install invokes the exact installed CLI and leaves the
    # generated desktop executable beside (not inside) the CLI directory.
    $freshLocal = Join-Path $root 'fresh-localappdata'
    New-Item -ItemType Directory -Path $freshLocal -Force | Out-Null
    $freshLog = Join-Path $root 'fresh.log'
    $freshUserPath = '%SystemRoot%\System32;;C:\unrelated-tools;'
    Set-AetherUserPath $freshUserPath
    Write-AetherRelease $releaseRoot $version $fixture $fixture
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version) @{
        AETHER_BASE_URL = $baseUrl
        LOCALAPPDATA = $freshLocal
        AETHER_FIXTURE_LOG = $freshLog
        AETHER_DESKTOP_TARGET = (Join-Path $freshLocal 'Programs\Aether Desktop\aether-desktop.exe')
        AETHER_BIN = 'caller-sentinel'
    }
    $freshBin = Join-Path $freshLocal 'Programs\Aether'
    $freshCli = Join-Path $freshBin 'aether.exe'
    $freshDesktop = Join-Path $freshLocal 'Programs\Aether Desktop\aether-desktop.exe'
    Assert-AetherEqual 0 $result.ExitCode ("fresh default install failed: " + $result.Output)
    Assert-AetherTest (Get-AetherTestBytesEqual $fixture $freshCli) 'fresh install did not retain the CLI executable'
    Assert-AetherTest (Test-Path -LiteralPath $freshDesktop -PathType Leaf) 'GUI build did not create the sibling desktop executable'
    foreach ($expected in @(
            '[3/3] installing the desktop app (the build output follows)',
            'fixture stdout progress',
            ('      installed {0}' -f $freshDesktop),
            'aether link --claim <code>',
            'aether link <server-host>:2222 # instead, on a tailnet or without the edge',
            ('done: Aether {0} installed in ' -f $version))) {
        Assert-AetherTest ($result.Stdout.Contains($expected)) ("fresh install output lacks [{0}]: {1}" -f $expected, $result.Output)
    }
    Assert-AetherTest ($result.Stderr.Contains('fixture stderr progress')) ("desktop build stderr did not reach the console: " + $result.Output)
    Assert-AetherTest (-not $result.Stderr.Contains('NativeCommandError')) ("desktop build stderr became an error record: " + $result.Stderr)
    Assert-AetherTest ((Get-AetherTestUserPath).StartsWith($freshUserPath)) 'fresh install altered unrelated PATH entries'
    Assert-AetherTest ([Environment]::GetEnvironmentVariable('Path', 'User').StartsWith([Environment]::ExpandEnvironmentVariables($freshUserPath))) 'fresh install stopped expanding PATH tokens'

    # Reinstall twice: unrelated files survive and a case/trailing-slash PATH
    # entry is recognized without destroying expandable tokens.
    $unrelated = Join-Path $freshBin 'keep-me.txt'
    [IO.File]::WriteAllText($unrelated, 'unrelated user file')
    $tokenPath = '%SystemRoot%\System32;' + $freshBin.ToUpperInvariant() + '\'
    Set-AetherUserPath $tokenPath
    $reinstallLog = Join-Path $root 'reinstall.log'
    Write-AetherRelease $releaseRoot $version $fixture $fixture
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', ($freshBin + '\')) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $reinstallLog
    }
    Assert-AetherEqual 0 $result.ExitCode ("first reinstall failed: " + $result.Output)
    [IO.File]::WriteAllText($requestLog, '')
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $freshBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $reinstallLog
    }
    Assert-AetherEqual 0 $result.ExitCode ("second reinstall failed: " + $result.Output)
    Assert-AetherTest ($result.Stdout.Contains(('[2/2] CLI {0} is already installed' -f $version))) ("unchanged reinstall did not say so: " + $result.Output)
    Assert-AetherTest (-not ([IO.File]::ReadAllText($requestLog).Contains('aether-windows-amd64.exe'))) 'unchanged reinstall downloaded the CLI again'
    Assert-AetherEqual 'unrelated user file' ([IO.File]::ReadAllText($unrelated)) 'reinstall deleted an unrelated file'
    $finalUserPath = Get-AetherTestUserPath
    Assert-AetherTest ($finalUserPath.Contains('%SystemRoot%\System32')) 'reinstall lost an expandable PATH token'
    $normalizedEntries = @($finalUserPath -split ';' | ForEach-Object { Get-AetherNormalizedPathEntry $_ })
    $normalizedBin = Get-AetherNormalizedPathEntry $freshBin
    Assert-AetherEqual 1 (@($normalizedEntries | Where-Object { $_ -eq $normalizedBin }).Count) 'reinstall duplicated the install directory in PATH'

    # -Role none permits historical releases without invoking the fixture/build.
    $noneBin = Join-Path $root 'none-bin'
    $noneLog = Join-Path $root 'none.log'
    New-Item -ItemType Directory -Path $noneBin -Force | Out-Null
    Write-AetherRelease $releaseRoot 'v0.4.0-alpha.6' $fixture $fixture
    $result = Invoke-AetherPowerShellFile $installer @('-Version', 'v0.4.0-alpha.6', '-Role', 'none', '-BinDir', $noneBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $noneLog
    }
    Assert-AetherEqual 0 $result.ExitCode ("CLI-only install failed: " + $result.Output)
    Assert-AetherTest (Test-Path -LiteralPath (Join-Path $noneBin 'aether.exe') -PathType Leaf) 'CLI-only install did not install the CLI'
    Assert-AetherTest (-not (Test-Path -LiteralPath $noneLog)) 'CLI-only opt out invoked GUI build'

    # A desktop failure leaves the newly installed CLI and restores AETHER_BIN.
    # Called from a script with & or dot-sourced, the installer prints the
    # error once and throws, so the caller's catch sees it.
    $failureCase = Join-Path $root 'failure-case'
    $failureBin = Join-Path $failureCase 'bin'
    New-Item -ItemType Directory -Path $failureBin -Force | Out-Null
    $failureLog = Join-Path $root 'failure.log'
    $restoreResult = Join-Path $root 'restored-aether-bin.txt'
    $installerLiteral = $installer.Replace("'", "''")
    $failureCaseLiteral = $failureCase.Replace("'", "''")
    $restoreLiteral = $restoreResult.Replace("'", "''")
    Write-AetherRelease $releaseRoot $version $fixture $fixture
    foreach ($operator in @('&', '.')) {
        $wrapper = Join-Path $root 'caller-wrapper.ps1'
        $wrapperSource = @"
`$env:AETHER_BIN = 'caller-sentinel'
Set-Location -LiteralPath '$failureCaseLiteral'
try {
    $operator '$installerLiteral' -Version '$version' -Role client -BinDir '.\bin'
    'caller was not told'
}
catch {
    'caller caught: ' + `$_.Exception.Message
}
[IO.File]::WriteAllText('$restoreLiteral', [Environment]::GetEnvironmentVariable('AETHER_BIN', 'Process'))
"@
        [IO.File]::WriteAllText($wrapper, $wrapperSource)
        Remove-Item -LiteralPath $restoreResult -Force -ErrorAction SilentlyContinue
        $result = Invoke-AetherPowerShellFile $wrapper @() @{
            AETHER_BASE_URL = $baseUrl
            AETHER_FIXTURE_LOG = $failureLog
            AETHER_GUI_FAIL = '1'
        }
        Assert-AetherEqual 0 $result.ExitCode ("wrapper under {0} failed: {1}" -f $operator, $result.Output)
        Assert-AetherTest ($result.Stdout.Contains('caller caught: aether gui build exited with code 23. The CLI is installed; rerun: ')) ("installer under {0} did not throw to its caller: {1}" -f $operator, $result.Output)
        Assert-AetherTest ($result.Stderr.Contains('aether install: error: aether gui build exited with code 23')) ("desktop failure was not reported: " + $result.Output)
        Assert-AetherTest ($result.Stderr.Contains('fixture desktop failure')) ("desktop failure output did not reach the console: " + $result.Output)
        Assert-AetherEqual 1 ([regex]::Matches($result.Stderr, 'exited with code 23').Count) ("desktop failure under {0} printed the error more than once: {1}" -f $operator, $result.Stderr)
        Assert-AetherTest (Test-Path -LiteralPath (Join-Path $failureBin 'aether.exe') -PathType Leaf) 'desktop failure lost the installed CLI'
        Assert-AetherEqual 'caller-sentinel' ([IO.File]::ReadAllText($restoreResult)) ("installer under {0} did not restore AETHER_BIN" -f $operator)
    }

    # Old client releases are rejected before download/install/build mutation;
    # role none remains the deliberate escape hatch for an old CLI.
    $unsafeBin = Join-Path $root 'unsafe-bin'
    New-Item -ItemType Directory -Path $unsafeBin -Force | Out-Null
    $unsafeCli = Join-Path $unsafeBin 'aether.exe'
    [IO.File]::WriteAllText($unsafeCli, 'old unsafe CLI')
    $unsafeLog = Join-Path $root 'unsafe.log'
    $unsafeBeforePath = '%SystemRoot%\System32;C:\safe-tools'
    foreach ($oldVersion in @('v0.4.0-alpha.5', 'v0.4.0-alpha.6', 'v0.5.0-alpha.1', 'v0.5.1-alpha.3')) {
        Set-AetherUserPath $unsafeBeforePath
        [IO.File]::WriteAllText($requestLog, '')
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $oldVersion, '-Role', 'client', '-BinDir', $unsafeBin) @{
            AETHER_BASE_URL = $baseUrl
            AETHER_FIXTURE_LOG = $unsafeLog
        }
        Assert-AetherTest ($result.ExitCode -ne 0) ("unsafe old client release was accepted: " + $oldVersion)
        Assert-AetherEqual 'old unsafe CLI' ([IO.File]::ReadAllText($unsafeCli)) 'unsafe release mutated the CLI'
        Assert-AetherEqual $unsafeBeforePath (Get-AetherTestUserPath) 'unsafe release changed user PATH'
        Assert-AetherEqual '' ([IO.File]::ReadAllText($requestLog)) 'unsafe release requested a download'
        Assert-AetherTest (-not (Test-Path -LiteralPath $unsafeLog)) 'unsafe release executed the fixture'
    }

    # The exact floor is exercised by the fresh install above; later numeric
    # prereleases and stable releases must also install and build successfully.
    foreach ($supportedVersion in @('v0.5.1-alpha.10', 'v0.5.1')) {
        $supportedBin = Join-Path $root $supportedVersion
        $supportedLog = Join-Path $supportedBin 'build.log'
        New-Item -ItemType Directory -Path $supportedBin -Force | Out-Null
        Write-AetherRelease $releaseRoot $supportedVersion $fixture $fixture
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $supportedVersion, '-Role', 'client', '-BinDir', $supportedBin) @{
            AETHER_BASE_URL = $baseUrl
            AETHER_FIXTURE_LOG = $supportedLog
        }
        Assert-AetherEqual 0 $result.ExitCode ("supported release install failed: " + $result.Output)
        Assert-AetherTest (Get-AetherTestBytesEqual $fixture (Join-Path $supportedBin 'aether.exe')) 'supported release did not install the CLI'
        Assert-AetherTest (Test-Path -LiteralPath $supportedLog -PathType Leaf) 'supported release did not invoke GUI build'
    }

    # Simulate 32-bit PowerShell on a 64-bit ARM host.  The native override is
    # authoritative and only the arm64 asset may be requested/installed.
    $archBin = Join-Path $root 'architecture-bin'
    New-Item -ItemType Directory -Path $archBin -Force | Out-Null
    $archLog = Join-Path $root 'architecture.log'
    [IO.File]::WriteAllText($requestLog, '')
    Write-AetherRelease $releaseRoot $version $fixture 'arm-native-payload'
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $archBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $archLog
        PROCESSOR_ARCHITEW6432 = 'ARM64'
        PROCESSOR_ARCHITECTURE = 'x86'
    }
    Assert-AetherEqual 0 $result.ExitCode ("native architecture detection install failed: " + $result.Output)
    Assert-AetherEqual 'arm-native-payload' ([IO.File]::ReadAllText((Join-Path $archBin 'aether.exe'))) 'native architecture detection selected the wrong asset'
    $requests = [IO.File]::ReadAllText($requestLog)
    Assert-AetherTest ($requests.Contains('aether-windows-arm64.exe')) 'architecture test did not request arm64 asset'
    Assert-AetherTest (-not $requests.Contains('aether-windows-amd64.exe')) 'architecture test requested the emulated x86 asset'

    # Release selection.  Payloads are text because -Role none never runs them,
    # which lets each assertion name the exact release that was installed.
    $selectBin = Join-Path $root 'select-bin'
    New-Item -ItemType Directory -Path $selectBin -Force | Out-Null
    $selectCli = Join-Path $selectBin 'aether.exe'
    $selectEnv = @{
        AETHER_BASE_URL = $baseUrl
        AETHER_LATEST_URL = ($baseUrl + '/latest')
        AETHER_API_URL = ($baseUrl + '/api')
    }
    foreach ($tag in @('v0.6.0', 'v0.7.0-alpha.8', 'v0.7.0-alpha.9', 'v0.7.0-alpha.10', 'v0.7.0-alpha.11', 'v0.7.0-alpha.13', 'v0.7.0-alpha.14', 'v0.7.0-alpha.15', 'v0.7.0-alpha.16', 'v0.7.0-alpha.17', 'v0.7.0-alpha.18', 'nightly')) {
        $omit = @()
        $badChecksum = $false
        $manifestVersion = $tag
        switch ($tag) {
            'v0.6.0' { $omit = @('aether-release.json', 'aether-release.json.sig') }
            'v0.7.0-alpha.8' { $omit = @('aether-release.json.sig') }
            'v0.7.0-alpha.10' { $omit = @('aether-windows-amd64.exe') }
            'v0.7.0-alpha.11' { $omit = @('aether-release.json.sig') }
            'v0.7.0-alpha.17' { $omit = @('aether-release.json') }
            'v0.7.0-alpha.18' { $omit = @('aether-release.json', 'aether-release.json.sig') }
            'v0.7.0-alpha.15' { $badChecksum = $true }
            'v0.7.0-alpha.16' { $manifestVersion = 'v0.7.0-alpha.9' }
        }
        Write-AetherRelease $releaseRoot $tag ('cli ' + $tag) ('arm ' + $tag) -BadAmdChecksum $badChecksum -ManifestVersion $manifestVersion -Omit $omit
    }
    $allTags = @('nightly', 'v0.7.0-alpha.18', 'v0.7.0-alpha.17', 'v0.7.0-alpha.14', 'v0.7.0-alpha.13', 'v0.7.0-alpha.12', 'v0.7.0-alpha.11', 'v0.7.0-alpha.10', 'v0.7.0-alpha.9', 'v0.7.0-alpha.8', 'v0.6.0')
    Write-AetherApi $releaseRoot -Tags $allTags -Drafts @('v0.7.0-alpha.13') -Prereleases @('v0.7.0-alpha.14')

    $fallbackCases = @(
        @{ Latest = 'v0.7.0-alpha.12'; Reason = 'no checksums.txt' },
        @{ Latest = 'v0.7.0-alpha.11'; Reason = 'no aether-release.json.sig' },
        @{ Latest = 'v0.7.0-alpha.17'; Reason = 'no aether-release.json' },
        @{ Latest = 'v0.7.0-alpha.18'; Reason = 'no aether-release.json' },
        @{ Latest = 'v0.7.0-alpha.10'; Reason = 'no aether-windows-amd64.exe' }
    )
    foreach ($case in $fallbackCases) {
        [IO.File]::WriteAllText($selectCli, 'old select CLI')
        [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), $case.Latest)
        [IO.File]::WriteAllText($requestLog, '')
        $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
        Assert-AetherEqual 0 $result.ExitCode ("fallback from {0} failed: {1}" -f $case.Latest, $result.Output)
        Assert-AetherEqual 'cli v0.7.0-alpha.9' ([IO.File]::ReadAllText($selectCli)) ("fallback from {0} installed the wrong release" -f $case.Latest)
        $expected = '{0} is not fully published yet ({1}); looking for the newest complete release' -f $case.Latest, $case.Reason
        Assert-AetherTest ($result.Stdout.Contains($expected)) ("fallback did not explain itself; expected [{0}] in: {1}" -f $expected, $result.Output)
        Assert-AetherTest ($result.Stdout.Contains('installing v0.7.0-alpha.9, the newest complete release')) ("fallback did not name the installed version: " + $result.Output)
        Assert-AetherTest ($result.Stdout.Contains('verified aether-windows-amd64.exe SHA-256 and size against checksums.txt and aether-release.json; aether-release.json.sig is not checked (PowerShell has no ed25519 verifier)')) ("fallback did not report the unverified signature: " + $result.Output)
        Assert-AetherTest (-not $result.Stdout.Contains('skipping')) ("fallback reported unprobed releases: " + $result.Output)
        $requests = @([IO.File]::ReadAllLines($requestLog))
        foreach ($ignored in @('v0.7.0-alpha.18/', 'v0.7.0-alpha.17/', 'v0.7.0-alpha.14/', 'v0.7.0-alpha.13/', 'v0.7.0-alpha.11/', 'v0.7.0-alpha.10/', 'nightly/', 'v0.7.0-alpha.8/', 'v0.6.0/')) {
            if ($ignored -eq ($case.Latest + '/')) { continue }
            Assert-AetherTest (@($requests | Where-Object { $_.StartsWith($ignored) }).Count -eq 0) ("fallback requested {0} assets: {1}" -f $ignored, ($requests -join ', '))
        }
    }

    # A pinned complete release installs without consulting latest or the API.
    [IO.File]::WriteAllText($requestLog, '')
    $result = Invoke-AetherPowerShellFile $installer @('-Version', 'v0.7.0-alpha.9', '-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Assert-AetherEqual 0 $result.ExitCode ("pinned complete release failed: " + $result.Output)
    Assert-AetherEqual 'cli v0.7.0-alpha.9' ([IO.File]::ReadAllText($selectCli)) 'pinned complete release installed the wrong CLI'
    $requests = @([IO.File]::ReadAllLines($requestLog))
    Assert-AetherTest (@($requests | Where-Object { -not $_.StartsWith('v0.7.0-alpha.9/') }).Count -eq 0) ("pinned release requested other paths: " + ($requests -join ', '))

    # The old CLI's version comes from its VERSIONINFO, never from running
    # it; without one, the upgrade names only the new version.
    $upgradeBin = Join-Path $root 'upgrade-bin'
    $upgradeLog = Join-Path $root 'upgrade.log'
    New-Item -ItemType Directory -Path $upgradeBin -Force | Out-Null
    Copy-Item -LiteralPath $fixture -Destination (Join-Path $upgradeBin 'aether.exe')
    $upgradeEnv = $selectEnv.Clone()
    $upgradeEnv.AETHER_FIXTURE_LOG = $upgradeLog
    $result = Invoke-AetherPowerShellFile $installer @('-Version', 'v0.7.0-alpha.9', '-Role', 'none', '-BinDir', $upgradeBin) $upgradeEnv
    Assert-AetherEqual 0 $result.ExitCode ("upgrade failed: " + $result.Output)
    Assert-AetherTest ($result.Stdout.Contains('[2/2] installing the CLI v0.7.0-alpha.9')) ("upgrade without VERSIONINFO named an old version: " + $result.Output)
    Assert-AetherTest (-not (Test-Path -LiteralPath $upgradeLog)) 'upgrade executed the old CLI'

    # A release from before v0.7.0-alpha.12 has neither manifest file; pinned
    # or latest, it installs on checksums.txt alone and says so.
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.6.0')
    foreach ($arguments in @(@('-Version', 'v0.6.0'), @())) {
        [IO.File]::WriteAllText($selectCli, 'old select CLI')
        [IO.File]::WriteAllText($requestLog, '')
        $result = Invoke-AetherPowerShellFile $installer ($arguments + @('-Role', 'none', '-BinDir', $selectBin)) $selectEnv
        Assert-AetherEqual 0 $result.ExitCode ("unsigned release failed: " + $result.Output)
        Assert-AetherEqual 'cli v0.6.0' ([IO.File]::ReadAllText($selectCli)) 'unsigned release installed the wrong CLI'
        Assert-AetherTest ($result.Stdout.Contains('verified aether-windows-amd64.exe SHA-256 against checksums.txt only: v0.6.0 predates release signing and has no aether-release.json')) ("unsigned release did not report checksum-only verification: " + $result.Output)
        Assert-AetherTest (-not ([IO.File]::ReadAllLines($requestLog) -contains 'api')) 'unsigned release consulted the API'
    }

    # Every failure below leaves the existing CLI in place.
    $failureCases = @(
        @{ Args = @('-Version', 'v0.7.0-alpha.12'); Message = 'v0.7.0-alpha.12 has no published checksums.txt; it may still be building or may have failed - not installing a different version'; Only = 'v0.7.0-alpha.12/' },
        @{ Args = @('-Version', 'v0.7.0-alpha.10'); Message = 'v0.7.0-alpha.10 has no published aether-windows-amd64.exe; it may still be building or may have failed - not installing a different version'; Only = 'v0.7.0-alpha.10/' },
        @{ Args = @('-Version', 'v0.7.0-alpha.8'); Message = 'v0.7.0-alpha.8 has no published aether-release.json.sig; it may still be building or may have failed - not installing a different version'; Only = 'v0.7.0-alpha.8/' },
        @{ Args = @('-Version', 'v0.7.0-alpha.17'); Message = 'v0.7.0-alpha.17 has no published aether-release.json; it may still be building or may have failed - not installing a different version'; Only = 'v0.7.0-alpha.17/' },
        @{ Args = @('-Version', 'v0.7.0-alpha.18'); Message = 'v0.7.0-alpha.18 has no published aether-release.json; it may still be building or may have failed - not installing a different version'; Only = 'v0.7.0-alpha.18/' },
        @{ Args = @(); Latest = 'v0.7.0-alpha.15'; Message = 'checksum mismatch for aether-windows-amd64.exe of v0.7.0-alpha.15'; NoApi = $true },
        @{ Args = @(); Latest = 'v0.7.0-alpha.16'; Message = "aether-release.json of v0.7.0-alpha.16 names version 'v0.7.0-alpha.9'"; NoApi = $true }
    )
    foreach ($case in $failureCases) {
        [IO.File]::WriteAllText($selectCli, 'old select CLI')
        if ($case.ContainsKey('Latest')) {
            [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), $case.Latest)
        }
        [IO.File]::WriteAllText($requestLog, '')
        $result = Invoke-AetherPowerShellFile $installer ($case.Args + @('-Role', 'none', '-BinDir', $selectBin)) $selectEnv
        Assert-AetherTest ($result.ExitCode -ne 0) ("expected failure succeeded: " + $case.Message)
        Assert-AetherTest ($result.Stderr.Contains($case.Message)) ("expected [{0}] in: {1}" -f $case.Message, $result.Output)
        Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) ("failure replaced the CLI: " + $case.Message)
        $requests = @([IO.File]::ReadAllLines($requestLog))
        if ($case.ContainsKey('Only')) {
            Assert-AetherTest (@($requests | Where-Object { -not $_.StartsWith($case.Only) }).Count -eq 0) ("pinned failure fell back: " + ($requests -join ', '))
        }
        if ($case.ContainsKey('NoApi')) {
            Assert-AetherTest (-not ($requests -contains 'api')) ("integrity failure fell back: " + ($requests -join ', '))
        }
    }

    [IO.File]::WriteAllText($selectCli, 'old select CLI')
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.7.0-alpha.12')
    Write-AetherApi $releaseRoot -Tags @('v0.7.0-alpha.12', 'v0.7.0-alpha.11')
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'an all-incomplete release list succeeded'
    Assert-AetherTest ($result.Stderr.Contains('no complete release for windows/amd64 among the newest 2 releases of 3xDevOps/Aether; pass -Version <tag>')) ("no-candidate failure was not explained: " + $result.Output)
    Assert-AetherTest ($result.Stdout.Contains('v0.7.0-alpha.12 is not fully published yet (no checksums.txt)')) ("no-candidate failure did not name the probed release: " + $result.Output)
    Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) 'no-candidate failure replaced the CLI'

    Write-AetherApi $releaseRoot -Status 403 -RateLimitRemaining '0'
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'a rate-limited API succeeded'
    Assert-AetherTest ($result.Stderr.Contains('GitHub API rate limit exceeded (HTTP 403, X-RateLimit-Remaining 0); pass -Version <tag> to install a specific release')) ("rate limit was not reported: " + $result.Output)
    Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) 'rate-limit failure replaced the CLI'

    $closed = New-Object System.Net.Sockets.TcpListener -ArgumentList @([Net.IPAddress]::Loopback, 0)
    $closed.Start()
    $closedUrl = 'http://127.0.0.1:{0}/latest' -f ([Net.IPEndPoint]$closed.LocalEndpoint).Port
    $closed.Stop()
    $unreachableEnv = $selectEnv.Clone()
    $unreachableEnv.AETHER_LATEST_URL = $closedUrl
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $unreachableEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'an unreachable GitHub succeeded'
    Assert-AetherTest ($result.Stderr.Contains('cannot resolve the latest release from ' + $closedUrl + ': ')) ("unreachable host did not name the URL: " + $result.Output)
    Assert-AetherTest (-not $result.Output.Contains('no complete release')) ("unreachable host was reported as no release: " + $result.Output)
    Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) 'unreachable host replaced the CLI'

    # Any other API error shows the body's message, not just the status line.
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.7.0-alpha.12')
    Write-AetherApi $releaseRoot -Status 500 -Message 'Server Error: try again'
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'a failing API succeeded'
    Assert-AetherTest ($result.Stderr.Contains('returned HTTP 500: ') -and $result.Stderr.Contains('Server Error: try again')) ("API error body was not shown: " + $result.Output)

    # The fallback only takes a signed release older than latest: an
    # unsigned one (v0.6.0) is never chosen in its place.
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.7.0-alpha.8')
    Write-AetherApi $releaseRoot -Tags @('v0.7.0-alpha.8', 'v0.6.0')
    [IO.File]::WriteAllText($requestLog, '')
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'fallback installed an unsigned release'
    Assert-AetherTest ($result.Stderr.Contains('no complete release for windows/amd64')) ("unsigned fallback was not refused: " + $result.Output)
    Assert-AetherTest (@([IO.File]::ReadAllLines($requestLog) | Where-Object { $_.StartsWith('v0.6.0/') }).Count -eq 0) 'fallback probed an unsigned release'
    Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) 'unsigned fallback replaced the CLI'

    # A candidate too old for desktop setup is skipped, not fatal.
    Write-AetherRelease $releaseRoot 'v0.5.0' 'cli v0.5.0' 'arm v0.5.0'
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.7.0-alpha.12')
    Write-AetherApi $releaseRoot -Tags @('v0.7.0-alpha.12', 'v0.5.0')
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'client', '-BinDir', $selectBin) $selectEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'a too-old fallback succeeded'
    Assert-AetherTest ($result.Stderr.Contains('no complete release for windows/amd64')) ("too-old fallback was not skipped: " + $result.Output)
    Assert-AetherTest (-not $result.Output.Contains('too old')) ("too-old fallback threw instead of skipping: " + $result.Output)

    # curl.exe ignores .curlrc: with one naming a dead proxy and the server
    # dropping every client but curl, the install still succeeds.
    $curlHome = Join-Path $root 'curl-home'
    New-Item -ItemType Directory -Path $curlHome -Force | Out-Null
    foreach ($name in @('.curlrc', '_curlrc')) {
        [IO.File]::WriteAllText((Join-Path $curlHome $name), "proxy = `"http://127.0.0.1:1`"`n")
    }
    $curlrcEnv = $selectEnv.Clone()
    $curlrcEnv.CURL_HOME = $curlHome
    $agentFile = Join-Path $releaseRoot 'only-agent.txt'
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'latest.txt'), 'v0.7.0-alpha.9')
    [IO.File]::WriteAllText($agentFile, 'curl/')
    [IO.File]::WriteAllText($selectCli, 'old select CLI')
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $curlrcEnv
    Assert-AetherEqual 0 $result.ExitCode ("curl.exe read a .curlrc: " + $result.Output)
    Assert-AetherEqual 'cli v0.7.0-alpha.9' ([IO.File]::ReadAllText($selectCli)) 'curl.exe install with a .curlrc installed the wrong CLI'

    # When curl.exe gets no HTTP answer (here the server drops it),
    # Invoke-WebRequest resolves and downloads the release instead.
    [IO.File]::WriteAllText($agentFile, 'Mozilla/')
    [IO.File]::WriteAllText($selectCli, 'old select CLI')
    $result = Invoke-AetherPowerShellFile $installer @('-Role', 'none', '-BinDir', $selectBin) $selectEnv
    Remove-Item -LiteralPath $agentFile -Force
    Assert-AetherEqual 0 $result.ExitCode ("Invoke-WebRequest fallback failed: " + $result.Output)
    Assert-AetherEqual 'cli v0.7.0-alpha.9' ([IO.File]::ReadAllText($selectCli)) 'Invoke-WebRequest fallback installed the wrong CLI'
    [IO.File]::WriteAllText($selectCli, 'old select CLI')

    # Only https, except the loopback fixture server, is accepted.
    $insecureEnv = $selectEnv.Clone()
    $insecureEnv.AETHER_BASE_URL = 'http://example.invalid/releases/download'
    [IO.File]::WriteAllText($requestLog, '')
    $result = Invoke-AetherPowerShellFile $installer @('-Version', 'v0.7.0-alpha.9', '-Role', 'none', '-BinDir', $selectBin) $insecureEnv
    Assert-AetherTest ($result.ExitCode -ne 0) 'an http mirror was accepted'
    Assert-AetherTest ($result.Stderr.Contains('refusing http://example.invalid/releases/download: releases are downloaded over https only')) ("http mirror was not refused: " + $result.Output)
    Assert-AetherEqual 'old select CLI' ([IO.File]::ReadAllText($selectCli)) 'http mirror replaced the CLI'
    Write-Output 'install-test: ok'
}
finally {
    Stop-AetherFixtureServer $server $stopPath
    try {
        if ($hadUserPath) {
            $userEnvironment.SetValue('Path', $originalUserPath, $originalUserPathKind)
        }
        else {
            $userEnvironment.DeleteValue('Path', $false)
        }
    }
    finally {
        $userEnvironment.Dispose()
    }
    if (Test-Path -LiteralPath $root) {
        Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
    }
}
