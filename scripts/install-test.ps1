<#
.SYNOPSIS
  Runs the standalone regression suite for scripts/install.ps1.

.DESCRIPTION
  This suite has no Pester or external test-framework dependency.  It compiles
  one tiny Go fixture (the fixture is never executed by the test harness
  itself), serves controlled release files from a local .NET HttpListener, and
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
    $argumentText = @('-NoProfile', '-NonInteractive', '-File', (Quote-AetherProcessArgument $ScriptPath))
    foreach ($argument in $Arguments) {
        $argumentText += (Quote-AetherProcessArgument ([string]$argument))
    }
    $start.Arguments = ($argumentText -join ' ')
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($name in @('AETHER_VERSION', 'AETHER_BIN_DIR', 'AETHER_ROLE', 'AETHER_REPO', 'AETHER_BASE_URL', 'AETHER_BIN', 'AETHER_FIXTURE_LOG', 'AETHER_DESKTOP_TARGET', 'AETHER_GUI_FAIL', 'PROCESSOR_ARCHITEW6432', 'PROCESSOR_ARCHITECTURE', 'LOCALAPPDATA')) {
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
                $name = [IO.Path]::GetFileName($relative)
                $file = Join-Path $Root $name
                if ((Test-Path -LiteralPath $file -PathType Leaf) -and ($name -eq 'checksums.txt' -or $name -match '^aether-windows-(amd64|arm64)\.exe$')) {
                    $body = [IO.File]::ReadAllBytes($file)
                    $context.Response.StatusCode = 200
                    $context.Response.ContentLength64 = $body.Length
                    $context.Response.OutputStream.Write($body, 0, $body.Length)
                }
                else {
                    $context.Response.StatusCode = 404
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

function Write-AetherRelease {
    param(
        [Parameter(Mandatory = $true)][string] $ReleaseRoot,
        [Parameter(Mandatory = $true)][string] $Fixture,
        [Parameter(Mandatory = $true)][string] $ArmPayload,
        [bool] $BadAmdChecksum = $false
    )
    Copy-Item -LiteralPath $Fixture -Destination (Join-Path $ReleaseRoot 'aether-windows-amd64.exe') -Force
    if (Test-Path -LiteralPath $ArmPayload -PathType Leaf) {
        Copy-Item -LiteralPath $ArmPayload -Destination (Join-Path $ReleaseRoot 'aether-windows-arm64.exe') -Force
    }
    else {
        [IO.File]::WriteAllText((Join-Path $ReleaseRoot 'aether-windows-arm64.exe'), $ArmPayload)
    }
    $amdHash = (Get-FileHash -LiteralPath (Join-Path $ReleaseRoot 'aether-windows-amd64.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $armHash = (Get-FileHash -LiteralPath (Join-Path $ReleaseRoot 'aether-windows-arm64.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($BadAmdChecksum) { $amdHash = ('0' * 64) }
    $lines = @(('{0}  aether-windows-amd64.exe' -f $amdHash))
    $lines += ('{0}  aether-windows-arm64.exe' -f $armHash)
    [IO.File]::WriteAllText((Join-Path $ReleaseRoot 'checksums.txt'), (($lines -join "`r`n") + "`r`n"))
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
    if strings.Join(os.Args[1:], " ") != "gui build" {
        fmt.Fprintln(os.Stderr, "unsupported fixture command")
        os.Exit(64)
    }
    logPath := os.Getenv("AETHER_FIXTURE_LOG")
    if logPath != "" {
        _ = os.WriteFile(logPath, []byte("invoked"), 0644)
    }
    if target := os.Getenv("AETHER_DESKTOP_TARGET"); target != "" && os.Getenv("AETHER_GUI_FAIL") == "" {
        _ = os.MkdirAll(filepath.Dir(target), 0755)
        _ = os.WriteFile(target, []byte("desktop fixture"), 0644)
    }
    if os.Getenv("AETHER_GUI_FAIL") != "" {
        os.Exit(23)
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
    $version = 'v0.4.0-alpha.6'

    # A poisoned checksum must not replace an existing executable, append PATH,
    # or execute the downloaded fixture.
    $checksumCase = Join-Path $root 'checksum-case'
    $checksumBin = Join-Path $checksumCase 'bin'
    New-Item -ItemType Directory -Path $checksumBin -Force | Out-Null
    $oldCli = Join-Path $checksumBin 'aether.exe'
    [IO.File]::WriteAllText($oldCli, 'old checksum-case CLI')
    $beforePath = '%SystemRoot%\System32;C:\unrelated-tools'
    Set-AetherUserPath $beforePath
    Write-AetherRelease $releaseRoot $fixture $fixture -BadAmdChecksum $true
    $checksumLog = Join-Path $checksumCase 'fixture.log'
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $checksumBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $checksumLog
    }
    Assert-AetherTest ($result.ExitCode -ne 0) 'bad checksum unexpectedly succeeded'
    Assert-AetherEqual 'old checksum-case CLI' ([IO.File]::ReadAllText($oldCli)) 'bad checksum replaced the existing CLI'
    Assert-AetherEqual $beforePath (Get-AetherTestUserPath) 'bad checksum changed user PATH'
    Assert-AetherTest (-not (Test-Path -LiteralPath $checksumLog)) 'bad checksum executed the fixture'
    Write-AetherRelease $releaseRoot $fixture $fixture
    $checksumPath = Join-Path $releaseRoot 'checksums.txt'
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

    Write-AetherRelease $releaseRoot $fixture $fixture
    $lockedFile = [IO.File]::Open($oldCli, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $checksumBin) @{
            AETHER_BASE_URL = $baseUrl
        }
        Assert-AetherTest ($result.ExitCode -ne 0) 'locked CLI replacement was reported as success'
    } finally {
        $lockedFile.Dispose()
    }
    Assert-AetherEqual 'old checksum-case CLI' ([IO.File]::ReadAllText($oldCli)) 'locked CLI was damaged'
    Assert-AetherEqual $beforePath (Get-AetherTestUserPath) 'locked CLI changed user PATH'

    # A fresh default install invokes the exact installed CLI and leaves the
    # generated desktop executable beside (not inside) the CLI directory.
    $freshLocal = Join-Path $root 'fresh-localappdata'
    New-Item -ItemType Directory -Path $freshLocal -Force | Out-Null
    $freshLog = Join-Path $root 'fresh.log'
    $freshUserPath = '%SystemRoot%\System32;;C:\unrelated-tools;'
    Set-AetherUserPath $freshUserPath
    Write-AetherRelease $releaseRoot $fixture $fixture
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
    Assert-AetherEqual 0 $result.ExitCode 'fresh default install failed'
    Assert-AetherTest (Get-AetherTestBytesEqual $fixture $freshCli) 'fresh install did not retain the CLI executable'
    Assert-AetherTest (Test-Path -LiteralPath $freshDesktop -PathType Leaf) 'GUI build did not create the sibling desktop executable'
    Assert-AetherTest ((Get-AetherTestUserPath).StartsWith($freshUserPath)) 'fresh install altered unrelated PATH entries'
    Assert-AetherTest ([Environment]::GetEnvironmentVariable('Path', 'User').StartsWith([Environment]::ExpandEnvironmentVariables($freshUserPath))) 'fresh install stopped expanding PATH tokens'

    # Reinstall twice: unrelated files survive and a case/trailing-slash PATH
    # entry is recognized without destroying expandable tokens.
    $unrelated = Join-Path $freshBin 'keep-me.txt'
    [IO.File]::WriteAllText($unrelated, 'unrelated user file')
    $tokenPath = '%SystemRoot%\System32;' + $freshBin.ToUpperInvariant() + '\'
    Set-AetherUserPath $tokenPath
    $reinstallLog = Join-Path $root 'reinstall.log'
    Write-AetherRelease $releaseRoot $fixture $fixture
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', ($freshBin + '\')) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $reinstallLog
    }
    Assert-AetherEqual 0 $result.ExitCode 'first reinstall failed'
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $freshBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $reinstallLog
    }
    Assert-AetherEqual 0 $result.ExitCode 'second reinstall failed'
    Assert-AetherEqual 'unrelated user file' ([IO.File]::ReadAllText($unrelated)) 'reinstall deleted an unrelated file'
    $finalUserPath = Get-AetherTestUserPath
    Assert-AetherTest ($finalUserPath.Contains('%SystemRoot%\System32')) 'reinstall lost an expandable PATH token'
    $normalizedEntries = @($finalUserPath -split ';' | ForEach-Object { Get-AetherNormalizedPathEntry $_ })
    $normalizedBin = Get-AetherNormalizedPathEntry $freshBin
    Assert-AetherEqual 1 (@($normalizedEntries | Where-Object { $_ -eq $normalizedBin }).Count) 'reinstall duplicated the install directory in PATH'

    # -Role none is a CLI-only opt out and must not invoke the fixture/build.
    $noneBin = Join-Path $root 'none-bin'
    $noneLog = Join-Path $root 'none.log'
    New-Item -ItemType Directory -Path $noneBin -Force | Out-Null
    Write-AetherRelease $releaseRoot $fixture $fixture
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $noneBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $noneLog
    }
    Assert-AetherEqual 0 $result.ExitCode 'CLI-only install failed'
    Assert-AetherTest (Test-Path -LiteralPath (Join-Path $noneBin 'aether.exe') -PathType Leaf) 'CLI-only install did not install the CLI'
    Assert-AetherTest (-not (Test-Path -LiteralPath $noneLog)) 'CLI-only opt out invoked GUI build'

    # A desktop failure is nonzero, leaves the newly installed CLI, and restores
    # AETHER_BIN even when the installer is dot-sourced by an interactive host.
    $failureBin = Join-Path $root 'failure-bin'
    New-Item -ItemType Directory -Path $failureBin -Force | Out-Null
    $failureLog = Join-Path $root 'failure.log'
    $restoreResult = Join-Path $root 'restored-aether-bin.txt'
    $wrapper = Join-Path $root 'dot-source-wrapper.ps1'
    $installerLiteral = $installer.Replace("'", "''")
    $failureBinLiteral = $failureBin.Replace("'", "''")
    $restoreLiteral = $restoreResult.Replace("'", "''")
    $wrapperSource = @"
`$env:AETHER_BIN = 'caller-sentinel'
try {
    . '$installerLiteral' -Version '$version' -Role client -BinDir '$failureBinLiteral'
}
catch {
    [IO.File]::WriteAllText('$restoreLiteral', [Environment]::GetEnvironmentVariable('AETHER_BIN', 'Process'))
    throw
}
[IO.File]::WriteAllText('$restoreLiteral', [Environment]::GetEnvironmentVariable('AETHER_BIN', 'Process'))
"@
    [IO.File]::WriteAllText($wrapper, $wrapperSource)
    Write-AetherRelease $releaseRoot $fixture $fixture
    $result = Invoke-AetherPowerShellFile $wrapper @() @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $failureLog
        AETHER_GUI_FAIL = '1'
    }
    Assert-AetherTest ($result.ExitCode -ne 0) 'desktop failure was reported as success'
    Assert-AetherTest (Test-Path -LiteralPath (Join-Path $failureBin 'aether.exe') -PathType Leaf) 'desktop failure lost the installed CLI'
    Assert-AetherEqual 'caller-sentinel' ([IO.File]::ReadAllText($restoreResult)) 'dot-sourced installer did not restore AETHER_BIN'

    # Old client releases are rejected before download/install/build mutation;
    # role none remains the deliberate escape hatch for an old CLI.
    $unsafeBin = Join-Path $root 'unsafe-bin'
    New-Item -ItemType Directory -Path $unsafeBin -Force | Out-Null
    $unsafeCli = Join-Path $unsafeBin 'aether.exe'
    [IO.File]::WriteAllText($unsafeCli, 'old unsafe CLI')
    $unsafeLog = Join-Path $root 'unsafe.log'
    $unsafeBeforePath = '%SystemRoot%\System32;C:\safe-tools'
    Set-AetherUserPath $unsafeBeforePath
    $result = Invoke-AetherPowerShellFile $installer @('-Version', 'v0.4.0-alpha.5', '-Role', 'client', '-BinDir', $unsafeBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $unsafeLog
    }
    Assert-AetherTest ($result.ExitCode -ne 0) 'unsafe old client release was accepted'
    Assert-AetherEqual 'old unsafe CLI' ([IO.File]::ReadAllText($unsafeCli)) 'unsafe release mutated the CLI'
    Assert-AetherEqual $unsafeBeforePath (Get-AetherTestUserPath) 'unsafe release changed user PATH'
    Assert-AetherTest (-not (Test-Path -LiteralPath $unsafeLog)) 'unsafe release executed the fixture'

    # Simulate 32-bit PowerShell on a 64-bit ARM host.  The native override is
    # authoritative and only the arm64 asset may be requested/installed.
    $archBin = Join-Path $root 'architecture-bin'
    New-Item -ItemType Directory -Path $archBin -Force | Out-Null
    $archLog = Join-Path $root 'architecture.log'
    [IO.File]::WriteAllText($requestLog, '')
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'aether-windows-arm64.exe'), 'arm-native-payload')
    $armHash = (Get-FileHash -LiteralPath (Join-Path $releaseRoot 'aether-windows-arm64.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $amdHash = (Get-FileHash -LiteralPath (Join-Path $releaseRoot 'aether-windows-amd64.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'checksums.txt'), ("{0}  aether-windows-amd64.exe`r`n{1}  aether-windows-arm64.exe`r`n" -f $amdHash, $armHash))
    $result = Invoke-AetherPowerShellFile $installer @('-Version', $version, '-Role', 'none', '-BinDir', $archBin) @{
        AETHER_BASE_URL = $baseUrl
        AETHER_FIXTURE_LOG = $archLog
        PROCESSOR_ARCHITEW6432 = 'ARM64'
        PROCESSOR_ARCHITECTURE = 'x86'
    }
    Assert-AetherEqual 0 $result.ExitCode 'native architecture detection install failed'
    Assert-AetherEqual 'arm-native-payload' ([IO.File]::ReadAllText((Join-Path $archBin 'aether.exe'))) 'native architecture detection selected the wrong asset'
    $requests = [IO.File]::ReadAllText($requestLog)
    Assert-AetherTest ($requests.Contains('aether-windows-arm64.exe')) 'architecture test did not request arm64 asset'
    Assert-AetherTest (-not $requests.Contains('aether-windows-amd64.exe')) 'architecture test requested the emulated x86 asset'
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
