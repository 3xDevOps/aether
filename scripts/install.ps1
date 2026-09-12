<#
.SYNOPSIS
  Installs the Aether Windows client and, by default, builds its desktop app.

.DESCRIPTION
  PowerShell 5.1 and PowerShell 7 are supported.  The installer deliberately
  uses only PowerShell/.NET facilities: there is no elevation, execution-policy
  change, Defender setting, or hidden child process.  Downloads are written to
  a temporary directory, checked against checksums.txt, then copied beside and
  atomically swapped into the installed CLI path.

.EXAMPLE
  powershell -NoProfile -File .\scripts\install.ps1

.EXAMPLE
  pwsh -NoProfile -File .\scripts\install.ps1 -Version v0.4.0-alpha.6 -Role none

.PARAMETER Version
  Release tag.  AETHER_VERSION is used when omitted; otherwise the latest
  GitHub release redirect is followed.

.PARAMETER BinDir
  CLI installation directory.  AETHER_BIN_DIR is used when omitted; otherwise
  %LOCALAPPDATA%\Programs\Aether is used.

.PARAMETER Role
  client (the default) installs the CLI and builds the desktop app.  none
  installs only the CLI.  AETHER_ROLE supplies the default.
#>
[CmdletBinding()]
param(
    [string] $Version,
    [string] $BinDir,
    [string] $Role
)

function Write-AetherMessage {
    param([string] $Message)
    Write-Output ("aether install: " + $Message)
}

function Get-AetherAbsolutePath {
    param([Parameter(Mandatory = $true)][string] $Path)
    try {
        return [System.IO.Path]::GetFullPath($Path)
    }
    catch {
        throw ("invalid path '{0}': {1}" -f $Path, $_.Exception.Message)
    }
}

function Remove-AetherTrailingSeparators {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string] $Path)
    $value = $Path
    while ($value.Length -gt 3 -and ($value.EndsWith('\') -or $value.EndsWith('/'))) {
        $value = $value.Substring(0, $value.Length - 1)
    }
    return $value
}

function Get-AetherPathComparisonValue {
    param([AllowEmptyString()][string] $Entry)
    if ($null -eq $Entry) {
        return ''
    }
    $value = $Entry.Trim()
    if ($value.Length -ge 2 -and $value.StartsWith('"') -and $value.EndsWith('"')) {
        $value = $value.Substring(1, $value.Length - 2)
    }
    $value = $value.Replace('/', '\')
    $value = Remove-AetherTrailingSeparators $value
    return $value
}

function Test-AetherPathEntryEquals {
    param(
        [AllowEmptyString()][string] $Entry,
        [Parameter(Mandatory = $true)][string] $Directory
    )
    $wanted = Get-AetherPathComparisonValue $Directory
    $candidate = Get-AetherPathComparisonValue $Entry
    if ([System.StringComparer]::OrdinalIgnoreCase.Equals($candidate, $wanted)) {
        return $true
    }
    # Keep the original PATH text untouched, but recognize an expandable user
    # entry such as %LOCALAPPDATA%\Programs\Aether as the install directory.
    if ($candidate -ne '') {
        try {
            $expanded = [Environment]::ExpandEnvironmentVariables($candidate)
            $expanded = Get-AetherPathComparisonValue $expanded
            return [System.StringComparer]::OrdinalIgnoreCase.Equals($expanded, $wanted)
        }
        catch {
            return $false
        }
    }
    return $false
}

function Test-AetherPathWithin {
    param(
        [Parameter(Mandatory = $true)][string] $Child,
        [Parameter(Mandatory = $true)][string] $Parent
    )
    $childValue = Remove-AetherTrailingSeparators (Get-AetherAbsolutePath $Child)
    $parentValue = Remove-AetherTrailingSeparators (Get-AetherAbsolutePath $Parent)
    if ([System.StringComparer]::OrdinalIgnoreCase.Equals($childValue, $parentValue)) {
        return $true
    }
    return $childValue.StartsWith($parentValue + '\', [System.StringComparison]::OrdinalIgnoreCase) -or
        $childValue.StartsWith($parentValue + '/', [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-AetherNativeArchitecture {
    # On 32-bit PowerShell under WOW64, PROCESSOR_ARCHITEW6432 is the native
    # architecture while PROCESSOR_ARCHITECTURE describes the host process.
    $candidates = @(
        [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432', 'Process'),
        [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE', 'Process')
    )
    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) {
            continue
        }
        switch ($candidate.Trim().ToUpperInvariant()) {
            'AMD64' { return 'amd64' }
            'X86_64' { return 'amd64' }
            'ARM64' { return 'arm64' }
            'AARCH64' { return 'arm64' }
            default { throw ("unsupported Windows architecture '{0}' (supported: amd64, arm64)" -f $candidate) }
        }
    }
    # RuntimeInformation is a native OS probe on modern .NET and is present
    # in both Windows PowerShell's framework and PowerShell 7's .NET runtime.
    try {
        $runtimeArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
        switch ($runtimeArchitecture.ToUpperInvariant()) {
            'X64' { return 'amd64' }
            'ARM64' { return 'arm64' }
        }
    }
    catch {
        # The environment probe above is available on supported Windows hosts;
        # retain a useful unsupported-architecture error if it is not.
    }
    $reported = ($candidates | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($reported)) {
        $reported = 'unknown'
    }
    throw ("unsupported Windows architecture '{0}' (supported: amd64, arm64)" -f $reported)
}

function Get-AetherSemVer {
    param([Parameter(Mandatory = $true)][string] $Tag)
    $pattern = '^(?:v)?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$'
    if ($Tag -notmatch $pattern) {
        return $null
    }
    $major = $Matches[1]
    $minor = $Matches[2]
    $patch = $Matches[3]
    $pre = $Matches[4]
    $preParts = @()
    if ($null -ne $pre -and $pre -ne '') {
        $preParts = $pre -split '\.'
        foreach ($part in $preParts) {
            if ($part -match '^[0-9]+$' -and $part.Length -gt 1 -and $part.StartsWith('0')) {
                return $null
            }
        }
    }
    return [pscustomobject]@{
        Major = $major
        Minor = $minor
        Patch = $patch
        PreRelease = $preParts
        HasPreRelease = ($null -ne $pre -and $pre -ne '')
    }
}

function Compare-AetherNumericStrings {
    param([Parameter(Mandatory = $true)][string] $Left, [Parameter(Mandatory = $true)][string] $Right)
    if ($Left.Length -ne $Right.Length) {
        if ($Left.Length -lt $Right.Length) { return -1 }
        return 1
    }
    return [System.String]::CompareOrdinal($Left, $Right)
}

function Compare-AetherSemVer {
    param([Parameter(Mandatory = $true)] $Left, [Parameter(Mandatory = $true)] $Right)
    foreach ($property in @('Major', 'Minor', 'Patch')) {
        $comparison = Compare-AetherNumericStrings $Left.$property $Right.$property
        if ($comparison -ne 0) { return $comparison }
    }
    if (-not $Left.HasPreRelease -and -not $Right.HasPreRelease) { return 0 }
    if (-not $Left.HasPreRelease) { return 1 }
    if (-not $Right.HasPreRelease) { return -1 }
    $count = [Math]::Min($Left.PreRelease.Count, $Right.PreRelease.Count)
    for ($i = 0; $i -lt $count; $i++) {
        $leftPart = [string]$Left.PreRelease[$i]
        $rightPart = [string]$Right.PreRelease[$i]
        $leftNumeric = $leftPart -match '^[0-9]+$'
        $rightNumeric = $rightPart -match '^[0-9]+$'
        if ($leftNumeric -and $rightNumeric) {
            $comparison = Compare-AetherNumericStrings $leftPart $rightPart
        }
        elseif ($leftNumeric -and -not $rightNumeric) {
            $comparison = -1
        }
        elseif (-not $leftNumeric -and $rightNumeric) {
            $comparison = 1
        }
        else {
            $comparison = [System.String]::CompareOrdinal($leftPart, $rightPart)
        }
        if ($comparison -ne 0) { return $comparison }
    }
    if ($Left.PreRelease.Count -lt $Right.PreRelease.Count) { return -1 }
    if ($Left.PreRelease.Count -gt $Right.PreRelease.Count) { return 1 }
    return 0
}

function Assert-AetherTag {
    param([Parameter(Mandatory = $true)][string] $Tag, [Parameter(Mandatory = $true)][string] $Role)
    if ($Role -ne 'client') {
        return
    }
    $minimum = Get-AetherSemVer 'v0.4.0-alpha.6'
    $actual = Get-AetherSemVer $Tag
    if ($null -eq $actual) {
        throw ("release tag '{0}' is not a semantic version; refusing desktop setup (pass -Role none to install the CLI without building)" -f $Tag)
    }
    if ((Compare-AetherSemVer $actual $minimum) -lt 0) {
        throw ("release {0} is too old for the separate Windows desktop layout; client installs require v0.4.0-alpha.6 or newer (pass -Role none to install the CLI without building)" -f $Tag)
    }
}

function Resolve-AetherLatestVersion {
    param([Parameter(Mandatory = $true)][string] $Repo)
    $uri = 'https://github.com/{0}/releases/latest' -f $Repo
    try {
        $response = Invoke-WebRequest -Uri $uri -UseBasicParsing -MaximumRedirection 20 -ErrorAction Stop
        $effective = if ($PSVersionTable.PSEdition -eq 'Desktop') {
            $response.BaseResponse.ResponseUri.AbsoluteUri
        } else {
            $response.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
        }
        if ([string]::IsNullOrWhiteSpace($effective)) {
            throw 'the response did not expose its final release URL'
        }
        $tag = [System.Uri]$effective
        $segments = $tag.Segments
        if ($segments.Count -lt 1) { throw 'the response URL had no release tag' }
        $value = [Uri]::UnescapeDataString($segments[$segments.Count - 1].TrimEnd('/'))
        if ([string]::IsNullOrWhiteSpace($value) -or $value -eq 'latest') {
            throw 'the response did not redirect to a published tag'
        }
        return $value
    }
    catch {
        throw ("cannot resolve the latest release for {0}: {1}" -f $Repo, $_.Exception.Message)
    }
}

function Assert-AetherReleaseTagForUri {
    param([Parameter(Mandatory = $true)][string] $Tag)
    if ([string]::IsNullOrWhiteSpace($Tag) -or $Tag -match '[\\/\?#]') {
        throw ("invalid release tag '{0}'" -f $Tag)
    }
}

function Get-AetherChecksum {
    param(
        [Parameter(Mandatory = $true)][string] $ChecksumPath,
        [Parameter(Mandatory = $true)][string] $Asset
    )
    $checksumMatches = @()
    $lineNumber = 0
    foreach ($line in ([System.IO.File]::ReadAllLines($ChecksumPath))) {
        $lineNumber++
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line -notmatch '^\s*([0-9A-Fa-f]{64})\s+(\*?[^\s]+)\s*$') {
            throw ("malformed checksum entry on line {0} of checksums.txt" -f $lineNumber)
        }
        $filename = $Matches[2]
        if ($filename.StartsWith('*')) { $filename = $filename.Substring(1) }
        if ($filename -ceq $Asset) {
            $checksumMatches += $Matches[1].ToLowerInvariant()
        }
    }
    if ($checksumMatches.Count -eq 0) {
        throw ("{0} is not listed in checksums.txt" -f $Asset)
    }
    if ($checksumMatches.Count -ne 1) {
        throw ("checksums.txt contains duplicate entries for {0}" -f $Asset)
    }
    return $checksumMatches[0]
}

function Download-AetherFile {
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)][string] $Path)
    Write-AetherMessage ("downloading {0}" -f $Uri)
    Invoke-WebRequest -Uri $Uri -UseBasicParsing -MaximumRedirection 20 -OutFile $Path -ErrorAction Stop
}

function Install-AetherCli {
    param(
        [Parameter(Mandatory = $true)][string] $DownloadedPath,
        [Parameter(Mandatory = $true)][string] $Directory,
        [Parameter(Mandatory = $true)][string] $ExpectedHash
    )
    $destination = Join-Path $Directory 'aether.exe'
    if (Test-Path -LiteralPath $destination -PathType Container) {
        throw ("cannot install CLI: {0} is a directory" -f $destination)
    }
    if (Test-Path -LiteralPath $Directory -PathType Leaf) {
        throw ("cannot install CLI: {0} is a file" -f $Directory)
    }
    New-Item -ItemType Directory -Path $Directory -Force -ErrorAction Stop | Out-Null
    $stage = Join-Path $Directory ('.aether-install-{0}.tmp' -f ([Guid]::NewGuid().ToString('N')))
    try {
        [System.IO.File]::Copy($DownloadedPath, $stage, $false)
        $stageHash = (Get-FileHash -LiteralPath $stage -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
        if ($stageHash -cne $ExpectedHash) {
            throw ("staged CLI checksum mismatch: expected {0}, got {1}" -f $ExpectedHash, $stageHash)
        }
        if (Test-Path -LiteralPath $destination -PathType Container) {
            throw ("cannot replace CLI: {0} is a directory" -f $destination)
        }
        if (Test-Path -LiteralPath $destination) {
            # File.Replace is a single filesystem operation and leaves the old
            # executable in place when Windows refuses a locked destination.
            [System.IO.File]::Replace($stage, $destination, $null, $true)
        }
        else {
            [System.IO.File]::Move($stage, $destination)
        }
    }
    catch {
        throw ("cannot atomically install {0}: {1}" -f $destination, $_.Exception.Message)
    }
    finally {
        if (Test-Path -LiteralPath $stage) {
            Remove-Item -LiteralPath $stage -Force -ErrorAction SilentlyContinue
        }
    }
    return $destination
}

function Invoke-AetherGuiBuild {
    param([Parameter(Mandatory = $true)][string] $CliPath)
    $hadAetherBin = Test-Path -LiteralPath 'Env:AETHER_BIN'
    $oldAetherBin = [Environment]::GetEnvironmentVariable('AETHER_BIN', 'Process')
    $previousExit = Get-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
    $rerun = "& '{0}' gui build" -f $CliPath.Replace("'", "''")
    $oldGuiEap = $ErrorActionPreference
    try {
        [Environment]::SetEnvironmentVariable('AETHER_BIN', $CliPath, 'Process')
        $global:LASTEXITCODE = -1
        # npm/electron-builder normally use stderr for progress and warnings;
        # stream it without turning an otherwise successful build into an
        # installer error under Windows PowerShell 5.1.
        $ErrorActionPreference = 'Continue'
        & $CliPath gui build
        $exitCode = [int]$global:LASTEXITCODE
        if ($exitCode -eq -1) {
            throw ("aether gui build could not start. The CLI is installed; rerun: {0}" -f $rerun)
        }
        if ($exitCode -ne 0) {
            throw ("aether gui build exited with code {0}. The CLI is installed; rerun: {1}" -f $exitCode, $rerun)
        }
    }
    catch {
        if ($_.Exception.Message -like 'aether gui build exited with code*') {
            throw
        }
        throw ("aether gui build failed: {0}. The CLI is installed; rerun: {1}" -f $_.Exception.Message, $rerun)
    }
    finally {
        $ErrorActionPreference = $oldGuiEap
        if ($hadAetherBin) {
            [Environment]::SetEnvironmentVariable('AETHER_BIN', $oldAetherBin, 'Process')
        }
        else {
            Remove-Item -LiteralPath 'Env:AETHER_BIN' -Force -ErrorAction SilentlyContinue
        }
        if ($null -ne $previousExit) {
            $global:LASTEXITCODE = $previousExit.Value
        } else {
            Remove-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
        }
    }
}

function Get-AetherUserPathRaw {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
    if ($null -eq $key) {
        return $null
    }
    try {
        return $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    }
    finally {
        $key.Close()
    }
}

function Update-AetherPath {
    param([Parameter(Mandatory = $true)][string] $Directory)
    $userPath = Get-AetherUserPathRaw
    $userEntries = @()
    if ($null -ne $userPath -and $userPath.Length -gt 0) {
        $userEntries = $userPath -split ';'
    }
    $hasUserEntry = $false
    $keptUserEntries = @()
    foreach ($entry in $userEntries) {
        if (Test-AetherPathEntryEquals $entry $Directory) {
            if (-not $hasUserEntry) {
                $keptUserEntries += $entry
                $hasUserEntry = $true
            }
        }
        else {
            $keptUserEntries += $entry
        }
    }
    $newUserPath = $userPath
    if ($hasUserEntry) {
        $newUserPath = $keptUserEntries -join ';'
    }
    else {
        if ([string]::IsNullOrEmpty($newUserPath)) {
            $newUserPath = $Directory
        }
        elseif ($newUserPath.EndsWith(';')) {
            $newUserPath = $newUserPath + $Directory
        }
        else {
            $newUserPath = $newUserPath + ';' + $Directory
        }
    }
    if ($newUserPath -cne $userPath) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
        try {
            $kind = if ($key.GetValueNames() -contains 'Path') {
                $key.GetValueKind('Path')
            } else {
                [Microsoft.Win32.RegistryValueKind]::ExpandString
            }
            $key.SetValue('Path', $newUserPath, $kind)
        } finally {
            $key.Dispose()
        }
    }

    $processPath = [Environment]::GetEnvironmentVariable('Path', 'Process')
    $processEntries = @()
    if ($null -ne $processPath -and $processPath.Length -gt 0) {
        $processEntries = $processPath -split ';'
    }
    $hasProcessEntry = $false
    foreach ($entry in $processEntries) {
        if (Test-AetherPathEntryEquals $entry $Directory) {
            $hasProcessEntry = $true
            break
        }
    }
    if (-not $hasProcessEntry) {
        if ([string]::IsNullOrEmpty($processPath)) {
            $processPath = $Directory
        }
        elseif ($processPath.EndsWith(';')) {
            $processPath = $processPath + $Directory
        }
        else {
            $processPath = $processPath + ';' + $Directory
        }
        [Environment]::SetEnvironmentVariable('Path', $processPath, 'Process')
        $env:Path = $processPath
    }
}

function Warn-AetherShadowing {
    param([Parameter(Mandatory = $true)][string] $Directory, [Parameter(Mandatory = $true)][string] $CliPath)
    $pathValue = [Environment]::GetEnvironmentVariable('Path', 'Process')
    if ([string]::IsNullOrEmpty($pathValue)) { return }
    foreach ($entry in ($pathValue -split ';')) {
        if ([string]::IsNullOrWhiteSpace($entry)) { continue }
        if (Test-AetherPathEntryEquals $entry $Directory) { break }
        try {
            $expandedEntry = [Environment]::ExpandEnvironmentVariables((Get-AetherPathComparisonValue $entry))
            $candidate = Join-Path $expandedEntry 'aether.exe'
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                Write-Warning ("an existing CLI at {0} may shadow {1}; it was not removed" -f $candidate, $CliPath)
            }
        }
        catch {
            # A malformed unrelated PATH entry must remain untouched.
        }
    }
}

function Invoke-AetherInstall {
    param(
        [AllowNull()][string] $RequestedVersion,
        [AllowNull()][string] $RequestedBinDir,
        [AllowNull()][string] $RequestedRole
    )
    $ErrorActionPreference = 'Stop'
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'this installer supports Windows only'
    }
    $architecture = Get-AetherNativeArchitecture
    $repo = [Environment]::GetEnvironmentVariable('AETHER_REPO', 'Process')
    if ([string]::IsNullOrWhiteSpace($repo)) { $repo = '3xDevOps/Aether' }
    $baseUrl = [Environment]::GetEnvironmentVariable('AETHER_BASE_URL', 'Process')
    if ([string]::IsNullOrWhiteSpace($baseUrl)) { $baseUrl = 'https://github.com/{0}/releases/download' -f $repo }
    $baseUrl = $baseUrl.TrimEnd('/')

    $role = $RequestedRole
    if ([string]::IsNullOrWhiteSpace($role)) {
        $role = [Environment]::GetEnvironmentVariable('AETHER_ROLE', 'Process')
    }
    if ([string]::IsNullOrWhiteSpace($role)) { $role = 'client' }
    $role = $role.Trim().ToLowerInvariant()
    if ($role -ne 'client' -and $role -ne 'none') {
        throw ("unknown role '{0}' (use client or none)" -f $role)
    }

    $version = $RequestedVersion
    if ([string]::IsNullOrWhiteSpace($version)) {
        $version = [Environment]::GetEnvironmentVariable('AETHER_VERSION', 'Process')
    }
    if ([string]::IsNullOrWhiteSpace($version)) {
        Write-AetherMessage 'resolving latest release'
        $version = Resolve-AetherLatestVersion $repo
    }
    Assert-AetherReleaseTagForUri $version
    Assert-AetherTag $version $role

    $localAppData = [Environment]::GetEnvironmentVariable('LOCALAPPDATA', 'Process')
    if ([string]::IsNullOrWhiteSpace($localAppData)) {
        $localAppData = [Environment]::GetFolderPath('LocalApplicationData')
    }
    if ([string]::IsNullOrWhiteSpace($localAppData)) {
        throw 'LOCALAPPDATA is not set; pass -BinDir with an absolute path'
    }
    if (-not [System.IO.Path]::IsPathRooted($localAppData)) {
        throw ("LOCALAPPDATA must be an absolute path: {0}" -f $localAppData)
    }
    $localAppData = Get-AetherAbsolutePath $localAppData
    $binDir = $RequestedBinDir
    if ([string]::IsNullOrWhiteSpace($binDir)) {
        $binDir = [Environment]::GetEnvironmentVariable('AETHER_BIN_DIR', 'Process')
    }
    if ([string]::IsNullOrWhiteSpace($binDir)) {
        $binDir = Join-Path $localAppData 'Programs\Aether'
    }
    $binDir = [Environment]::ExpandEnvironmentVariables($binDir)
    $binDir = Remove-AetherTrailingSeparators (Get-AetherAbsolutePath $binDir)
    $desktopDir = Join-Path $localAppData 'Programs\Aether Desktop'
    $desktopBuildDir = Join-Path $localAppData 'aether\desktop-build'
    $nodeCacheDir = Join-Path $localAppData 'aether\node'
    foreach ($blocked in @($desktopDir, $desktopBuildDir, $nodeCacheDir)) {
        if (Test-AetherPathWithin $binDir $blocked) {
            throw ("refusing -BinDir {0}: it is inside the managed desktop or build cache {1}" -f $binDir, $blocked)
        }
    }

    Write-AetherMessage ("installing {0} for windows/{1}" -f $version, $architecture)
    $asset = 'aether-windows-{0}.exe' -f $architecture
    $destination = Join-Path $binDir 'aether.exe'
    $desktopPath = Join-Path $desktopDir 'aether-desktop.exe'
    $tempRoot = $null
    try {
        $tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('.aether-install-{0}' -f ([Guid]::NewGuid().ToString('N')))
        New-Item -ItemType Directory -Path $tempRoot -Force -ErrorAction Stop | Out-Null
        $checksumPath = Join-Path $tempRoot 'checksums.txt'
        $downloadPath = Join-Path $tempRoot $asset
        Download-AetherFile ("{0}/{1}/checksums.txt" -f $baseUrl, $version) $checksumPath
        $expected = Get-AetherChecksum $checksumPath $asset
        Download-AetherFile ("{0}/{1}/{2}" -f $baseUrl, $version, $asset) $downloadPath
        $actual = (Get-FileHash -LiteralPath $downloadPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
        if ($actual -cne $expected) {
            throw ("checksum mismatch for {0}: expected {1}, got {2}" -f $asset, $expected, $actual)
        }
        $destination = Install-AetherCli $downloadPath $binDir $expected
        Write-AetherMessage ("installed CLI at {0}" -f $destination)

        try {
            Update-AetherPath $binDir
        } catch {
            throw ("CLI installed at {0}, but could not update the user PATH: {1}" -f $destination, $_.Exception.Message)
        }
        Warn-AetherShadowing $binDir $destination
        Write-AetherMessage 'PATH is updated for this PowerShell session and future logins. Existing applications keep their previous PATH.'
        if ($role -eq 'client') {
            Write-AetherMessage 'building the desktop app; output follows'
            Invoke-AetherGuiBuild $destination
            Write-AetherMessage ("installed desktop at {0}; open Aether from the Start Menu" -f $desktopPath)
        } else {
            Write-AetherMessage ("CLI installed; next: & '{0}' version" -f $destination.Replace("'", "''"))
        }
    }
    finally {
        if ($null -ne $tempRoot -and (Test-Path -LiteralPath $tempRoot)) {
            Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

try {
    Invoke-AetherInstall $Version $BinDir $Role
}
catch {
    [Console]::Error.WriteLine(("aether install: {0}" -f $_.Exception.Message))
    # Throwing, rather than exit, leaves an interactive host alive when this
    # script is dot-sourced or invoked as a scriptblock. -File still returns a
    # nonzero process status for the real failure.
    throw
}
