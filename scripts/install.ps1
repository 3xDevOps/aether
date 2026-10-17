<#
.SYNOPSIS
  Installs the Aether Windows client and, by default, builds its desktop app.

.DESCRIPTION
  PowerShell 5.1 and PowerShell 7 are supported.  The installer deliberately
  uses only PowerShell/.NET facilities: there is no elevation, execution-policy
  change, Defender setting, or hidden child process.  Downloads are written to
  a temporary directory, checked against checksums.txt and aether-release.json,
  then copied beside and atomically swapped into the installed CLI path.

.EXAMPLE
  powershell -NoProfile -File .\scripts\install.ps1

.EXAMPLE
  pwsh -NoProfile -File .\scripts\install.ps1 -Version v0.4.0-alpha.6 -Role none

.PARAMETER Version
  Release tag.  AETHER_VERSION is used when omitted; otherwise the newest
  complete GitHub release is installed.  A pinned tag is installed exactly or
  not at all.

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
        $provider = $null
        $drive = $null
        $resolved = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path, [ref]$provider, [ref]$drive)
        if ($provider.Name -ne 'FileSystem') {
            throw 'installation paths must use the filesystem provider'
        }
        return [System.IO.Path]::GetFullPath($resolved)
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
    $minimum = Get-AetherSemVer 'v0.5.1-alpha.4'
    $actual = Get-AetherSemVer $Tag
    if ($null -eq $actual) {
        throw ("release tag '{0}' is not a semantic version; refusing desktop setup (pass -Role none to install the CLI without building)" -f $Tag)
    }
    if ((Compare-AetherSemVer $actual $minimum) -lt 0) {
        throw ("release {0} is too old for supported Windows desktop setup; client installs require v0.5.1-alpha.4 or newer for the separate desktop layout, direct Node build, native shortcut, and recorded CLI binding (pass -Role none to install the CLI without building)" -f $Tag)
    }
}

function Resolve-AetherLatestVersion {
    param([Parameter(Mandatory = $true)][string] $Uri)
    try {
        $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -MaximumRedirection 20 -ErrorAction Stop
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
        throw ("cannot resolve the latest release from {0}: {1}" -f $Uri, $_.Exception.Message)
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
        return $null
    }
    if ($checksumMatches.Count -ne 1) {
        throw ("checksums.txt contains duplicate entries for {0}" -f $Asset)
    }
    return $checksumMatches[0]
}

function Test-AetherSigningRequired {
    param([Parameter(Mandatory = $true)][string] $Tag)
    # Every release from v0.7.0-alpha.12 on is signed. Two missing manifest
    # files say nothing about age: a release being published, or one whose
    # manifest was deleted, looks the same. Only an older tag installs on
    # checksums.txt alone.
    $semver = Get-AetherSemVer $Tag
    if ($null -eq $semver) { return $true }
    return (Compare-AetherSemVer $semver (Get-AetherSemVer 'v0.7.0-alpha.12')) -ge 0
}

function Get-AetherHttpStatus {
    param([Parameter(Mandatory = $true)] $Exception)
    # Windows PowerShell raises WebException (HttpWebResponse) and PowerShell 7
    # raises HttpResponseException (HttpResponseMessage); both expose Response.
    if ($null -eq $Exception.Response) { return $null }
    return [int]$Exception.Response.StatusCode
}

function Test-AetherRateLimited {
    param([Parameter(Mandatory = $true)] $Exception, $Status)
    if ($Status -ne 403 -and $Status -ne 429) { return $false }
    $headers = $Exception.Response.Headers
    if ($headers -is [System.Net.WebHeaderCollection]) {
        return $headers['X-RateLimit-Remaining'] -eq '0'
    }
    $values = $null
    if ($headers.TryGetValues('X-RateLimit-Remaining', [ref]$values)) {
        return @($values)[0] -eq '0'
    }
    return $false
}

function Format-AetherRequestError {
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)] $Exception, $Status)
    if ($null -eq $Status) {
        return ("cannot reach {0}: {1}" -f $Uri, $Exception.Message)
    }
    return ("{0} returned HTTP {1}: {2}" -f $Uri, $Status, $Exception.Message)
}

function Receive-AetherFile {
    param(
        [Parameter(Mandatory = $true)][string] $Uri,
        [Parameter(Mandatory = $true)][string] $Path,
        [int] $NotFoundAttempts = 1
    )
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -Uri $Uri -UseBasicParsing -MaximumRedirection 20 -OutFile $Path -ErrorAction Stop
            return $true
        }
        catch {
            $status = Get-AetherHttpStatus $_.Exception
            if ($status -eq 404) {
                if ($attempt -ge $NotFoundAttempts) { return $false }
            }
            else {
                $retryable = $null -eq $status -or $status -eq 408 -or $status -ge 500 -or
                    ($status -eq 429 -and -not (Test-AetherRateLimited $_.Exception $status))
                if (-not $retryable -or $attempt -ge 3) {
                    throw (Format-AetherRequestError $Uri $_.Exception $status)
                }
            }
        }
        Start-Sleep -Seconds 2
    }
}

function Assert-AetherManifest {
    param(
        [Parameter(Mandatory = $true)][string] $Path,
        [Parameter(Mandatory = $true)][string] $Tag,
        [Parameter(Mandatory = $true)][string] $Asset,
        [Parameter(Mandatory = $true)][string] $Expected,
        [Parameter(Mandatory = $true)][string] $BinaryPath
    )
    try {
        $manifest = [System.IO.File]::ReadAllText($Path) | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        throw ("aether-release.json of {0} is not valid JSON: {1}" -f $Tag, $_.Exception.Message)
    }
    if ($manifest.schema -ne 1) {
        throw ("aether-release.json of {0} has schema {1}; this installer reads schema 1" -f $Tag, $manifest.schema)
    }
    if ([string]$manifest.version -cne $Tag) {
        throw ("aether-release.json of {0} names version '{1}'" -f $Tag, $manifest.version)
    }
    $entry = $null
    if ($null -ne $manifest.assets) {
        $entry = $manifest.assets.PSObject.Properties[$Asset]
    }
    if ($null -eq $entry) {
        throw ("aether-release.json of {0} does not list {1}" -f $Tag, $Asset)
    }
    if ([string]$entry.Value.sha256 -cne $Expected) {
        throw ("aether-release.json of {0} lists SHA-256 {1} for {2}, but checksums.txt and the download have {3}" -f $Tag, $entry.Value.sha256, $Asset, $Expected)
    }
    $size = (Get-Item -LiteralPath $BinaryPath -ErrorAction Stop).Length
    if ($null -eq $entry.Value.size -or [int64]$entry.Value.size -ne $size) {
        throw ("aether-release.json of {0} lists size {1} for {2}, but the download is {3} bytes" -f $Tag, $entry.Value.size, $Asset, $size)
    }
}

function Read-AetherRelease {
    param(
        [Parameter(Mandatory = $true)][string] $BaseUrl,
        [Parameter(Mandatory = $true)][string] $Tag,
        [Parameter(Mandatory = $true)][string] $Asset,
        [Parameter(Mandatory = $true)][string] $Directory,
        [Parameter(Mandatory = $true)][hashtable] $Release
    )
    $Release.Tag = $Tag
    $Release.Ready = $false
    $Release.Missing = $null
    $Release.Reason = $null
    New-Item -ItemType Directory -Path $Directory -Force -ErrorAction Stop | Out-Null
    $uri = '{0}/{1}/checksums.txt' -f $BaseUrl, $Tag
    Write-AetherMessage ("downloading {0}" -f $uri)
    if (-not (Receive-AetherFile $uri (Join-Path $Directory 'checksums.txt'))) {
        $Release.Missing = 'checksums.txt'
        $Release.Reason = 'no checksums.txt'
        return
    }
    $present = @{}
    foreach ($name in @('aether-release.json', 'aether-release.json.sig')) {
        $uri = '{0}/{1}/{2}' -f $BaseUrl, $Tag, $name
        Write-AetherMessage ("downloading {0}" -f $uri)
        $present[$name] = Receive-AetherFile $uri (Join-Path $Directory $name)
    }
    $signed = $present['aether-release.json']
    # Releases that predate signing have neither file; exactly one means the
    # upload is still in progress or was interrupted.
    if ($signed -ne $present['aether-release.json.sig']) {
        $Release.Missing = if ($signed) { 'aether-release.json.sig' } else { 'aether-release.json' }
        $Release.Reason = 'no ' + $Release.Missing
        return
    }
    if (-not $signed -and (Test-AetherSigningRequired $Tag)) {
        $Release.Missing = 'aether-release.json'
        $Release.Reason = 'no aether-release.json'
        return
    }
    $expected = Get-AetherChecksum (Join-Path $Directory 'checksums.txt') $Asset
    if ($null -eq $expected) {
        $Release.Reason = 'checksums.txt does not list ' + $Asset
        return
    }
    $binaryPath = Join-Path $Directory $Asset
    $uri = '{0}/{1}/{2}' -f $BaseUrl, $Tag, $Asset
    Write-AetherMessage ("downloading {0}" -f $uri)
    # checksums.txt already lists the binary, so a 404 may be CDN propagation.
    if (-not (Receive-AetherFile $uri $binaryPath 3)) {
        $Release.Missing = $Asset
        $Release.Reason = 'no ' + $Asset
        return
    }
    $actual = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
    if ($actual -cne $expected) {
        throw ("checksum mismatch for {0} of {1}: expected {2}, got {3}" -f $Asset, $Tag, $expected, $actual)
    }
    if ($signed) {
        Assert-AetherManifest (Join-Path $Directory 'aether-release.json') $Tag $Asset $expected $binaryPath
        Write-AetherMessage ("aether-release.json.sig not verified (PowerShell has no ed25519 verifier); verified {0} SHA-256 and size against checksums.txt and aether-release.json" -f $Asset)
    }
    else {
        Write-AetherMessage ("{0} has no signed aether-release.json (it predates release signing); verifying SHA-256 against checksums.txt only" -f $Tag)
    }
    $Release.Path = $binaryPath
    $Release.Expected = $expected
    $Release.Ready = $true
}

function Get-AetherReleaseList {
    param([Parameter(Mandatory = $true)][string] $Uri)
    try {
        $response = Invoke-RestMethod -Uri $Uri -UseBasicParsing -Headers @{ Accept = 'application/vnd.github+json' } -ErrorAction Stop
    }
    catch {
        $status = Get-AetherHttpStatus $_.Exception
        if (Test-AetherRateLimited $_.Exception $status) {
            throw ("GitHub API rate limit exceeded (HTTP {0}, X-RateLimit-Remaining 0); pass -Version <tag> to install a specific release" -f $status)
        }
        throw ("cannot list releases: {0}" -f (Format-AetherRequestError $Uri $_.Exception $status))
    }
    # Windows PowerShell returns a JSON array as one object; foreach unrolls it.
    foreach ($release in $response) { $release }
}

function Find-AetherCompleteRelease {
    param(
        [Parameter(Mandatory = $true)][string] $LatestUrl,
        [Parameter(Mandatory = $true)][string] $ApiUrl,
        [Parameter(Mandatory = $true)][string] $Repo,
        [Parameter(Mandatory = $true)][string] $BaseUrl,
        [Parameter(Mandatory = $true)][string] $Asset,
        [Parameter(Mandatory = $true)][string] $Architecture,
        [Parameter(Mandatory = $true)][string] $Role,
        [Parameter(Mandatory = $true)][string] $TempRoot,
        [Parameter(Mandatory = $true)][hashtable] $Release
    )
    Write-AetherMessage 'resolving latest release'
    $latest = Resolve-AetherLatestVersion $LatestUrl
    Assert-AetherReleaseTagForUri $latest
    Assert-AetherTag $latest $Role
    Read-AetherRelease $BaseUrl $latest $Asset (Join-Path $TempRoot $latest) $Release
    if ($Release.Ready) { return }
    $latestReason = $Release.Reason

    $releases = @(Get-AetherReleaseList $ApiUrl)
    $candidates = @()
    foreach ($entry in $releases) {
        $tag = [string]$entry.tag_name
        if ($entry.draft -eq $true -or $entry.prerelease -eq $true -or $tag -ceq $latest -or [string]::IsNullOrWhiteSpace($tag)) { continue }
        $semver = Get-AetherSemVer $tag
        if ($null -eq $semver) { continue }
        $candidates += [pscustomobject]@{ Tag = $tag; SemVer = $semver; Assets = @($entry.assets | ForEach-Object { [string]$_.name }) }
    }
    for ($i = 1; $i -lt $candidates.Count; $i++) {
        $current = $candidates[$i]
        $j = $i - 1
        while ($j -ge 0 -and (Compare-AetherSemVer $candidates[$j].SemVer $current.SemVer) -lt 0) {
            $candidates[$j + 1] = $candidates[$j]
            $j--
        }
        $candidates[$j + 1] = $current
    }
    foreach ($candidate in $candidates) {
        $assets = $candidate.Assets
        if ($assets -cnotcontains 'checksums.txt' -or $assets -cnotcontains $Asset -or
            (($assets -ccontains 'aether-release.json') -ne ($assets -ccontains 'aether-release.json.sig')) -or
            (($assets -cnotcontains 'aether-release.json') -and (Test-AetherSigningRequired $candidate.Tag))) {
            continue
        }
        Assert-AetherTag $candidate.Tag $Role
        Read-AetherRelease $BaseUrl $candidate.Tag $Asset (Join-Path $TempRoot $candidate.Tag) $Release
        if ($Release.Ready) {
            Write-AetherMessage ("{0} is not fully published yet ({1}); installing {2}, the newest complete release" -f $latest, $latestReason, $candidate.Tag)
            return
        }
        Write-AetherMessage ("skipping {0}: not fully published ({1})" -f $candidate.Tag, $Release.Reason)
    }
    Write-AetherMessage ("skipping {0}: not fully published ({1})" -f $latest, $latestReason)
    throw ("no complete release for windows/{0} among the newest {1} releases of {2}; pass -Version <tag>" -f $Architecture, $releases.Count, $Repo)
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
            [System.IO.File]::Replace($stage, $destination, [NullString]::Value, $true)
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
    $previousExit = Get-Variable LASTEXITCODE -Scope Global -ValueOnly -ErrorAction SilentlyContinue
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
            $global:LASTEXITCODE = $previousExit
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
    $latestUrl = [Environment]::GetEnvironmentVariable('AETHER_LATEST_URL', 'Process')
    if ([string]::IsNullOrWhiteSpace($latestUrl)) { $latestUrl = 'https://github.com/{0}/releases/latest' -f $repo }
    $apiUrl = [Environment]::GetEnvironmentVariable('AETHER_API_URL', 'Process')
    if ([string]::IsNullOrWhiteSpace($apiUrl)) { $apiUrl = 'https://api.github.com/repos/{0}/releases?per_page=100' -f $repo }

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
    $pinned = -not [string]::IsNullOrWhiteSpace($version)
    if ($pinned) {
        Assert-AetherReleaseTagForUri $version
        Assert-AetherTag $version $role
    }

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

    $asset = 'aether-windows-{0}.exe' -f $architecture
    $destination = Join-Path $binDir 'aether.exe'
    $desktopPath = Join-Path $desktopDir 'aether-desktop.exe'
    $tempRoot = $null
    try {
        $tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('.aether-install-{0}' -f ([Guid]::NewGuid().ToString('N')))
        New-Item -ItemType Directory -Path $tempRoot -Force -ErrorAction Stop | Out-Null
        $release = @{}
        if ($pinned) {
            Read-AetherRelease $baseUrl $version $asset (Join-Path $tempRoot $version) $release
            if ($null -ne $release.Missing) {
                throw ("{0} has no published {1}; it may still be building or may have failed - not installing a different version" -f $version, $release.Missing)
            }
            if (-not $release.Ready) {
                throw ("{0} is incomplete ({1}); it may still be building or may have failed - not installing a different version" -f $version, $release.Reason)
            }
        }
        else {
            Find-AetherCompleteRelease $latestUrl $apiUrl $repo $baseUrl $asset $architecture $role $tempRoot $release
        }
        Write-AetherMessage ("installing {0} for windows/{1}" -f $release.Tag, $architecture)
        $destination = Install-AetherCli $release.Path $binDir $release.Expected
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
