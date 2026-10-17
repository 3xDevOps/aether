<#
.SYNOPSIS
  Installs the Aether Windows client and, by default, its desktop app.

.DESCRIPTION
  PowerShell 5.1 and PowerShell 7 are supported.  There is no elevation,
  execution-policy change, or Defender setting.  Downloads use the Windows
  curl.exe when present, otherwise Invoke-WebRequest; they are written to a
  temporary directory, checked against checksums.txt and aether-release.json,
  then copied beside and atomically swapped into the installed CLI path.  The
  desktop app is installed by the new CLI's `gui build`, whose output goes to
  %LOCALAPPDATA%\aether\install-desktop.log.

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
  client (the default) installs the CLI and the desktop app.  none installs
  only the CLI.  AETHER_ROLE supplies the default.
#>
[CmdletBinding()]
param(
    [string] $Version,
    [string] $BinDir,
    [string] $Role
)

$script:AetherColor = [string]::IsNullOrEmpty($env:NO_COLOR) -and -not [Console]::IsOutputRedirected

function Write-AetherStep {
    param([Parameter(Mandatory = $true)][string] $Message)
    $script:AetherStep++
    Write-Host ("[{0}/{1}] {2}" -f $script:AetherStep, $script:AetherSteps, $Message)
}

function Write-AetherDetail {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string] $Message, [switch] $NoNewline)
    Write-Host ('      ' + $Message) -NoNewline:$NoNewline
}

function Write-AetherStatus {
    param(
        [Parameter(Mandatory = $true)][string] $Word,
        [Parameter(Mandatory = $true)][ConsoleColor] $Color,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string] $Message
    )
    if ($script:AetherColor) {
        Write-Host $Word -ForegroundColor $Color -NoNewline
    }
    else {
        Write-Host $Word -NoNewline
    }
    Write-Host (' ' + $Message)
}

function Format-AetherSize {
    param([Parameter(Mandatory = $true)][int64] $Bytes)
    if ($Bytes -ge 1MB) { return ('{0:0.0} MB' -f ($Bytes / 1MB)) }
    if ($Bytes -ge 1KB) { return ('{0:0.0} KB' -f ($Bytes / 1KB)) }
    return ('{0} B' -f $Bytes)
}

function Format-AetherSeconds {
    param([Parameter(Mandatory = $true)][System.Diagnostics.Stopwatch] $Watch)
    return ('{0:0.0}s' -f $Watch.Elapsed.TotalSeconds)
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

function Invoke-AetherCurl {
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)][string[]] $Arguments)
    # Never follow a redirect from https down to http; Assert-AetherHttpsUri
    # admits http only for loopback.
    $protocols = if ($Uri.StartsWith('https:', [System.StringComparison]::OrdinalIgnoreCase)) { '=https' } else { '=http,https' }
    # -q must come first: it keeps curl from reading a .curlrc/_curlrc whose
    # insecure or proxy setting would undo the TLS checks on checksums.txt.
    $Arguments = @('-q', '--connect-timeout', '15', '--speed-limit', '1024', '--speed-time', '60', '--proto-redir', $protocols) + $Arguments + @($Uri)
    $oldEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $previousExit = Get-Variable LASTEXITCODE -Scope Global -ValueOnly -ErrorAction SilentlyContinue
    try {
        $output = @(& $script:AetherCurl @Arguments 2>&1)
        $code = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $oldEap
        if ($null -ne $previousExit) {
            $global:LASTEXITCODE = $previousExit
        }
        else {
            Remove-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
        }
    }
    $stdout = @($output | Where-Object { $_ -isnot [System.Management.Automation.ErrorRecord] }) -join "`n"
    $stderr = @($output | Where-Object { $_ -is [System.Management.Automation.ErrorRecord] } | ForEach-Object { $_.Exception.Message }) -join ' '
    return [pscustomobject]@{ ExitCode = $code; Stdout = $stdout.Trim(); Stderr = $stderr.Trim() }
}

function Resolve-AetherLatestVersion {
    param([Parameter(Mandatory = $true)][string] $Uri)
    try {
        $effective = $null
        # Follow the redirects one HEAD request at a time and stop at the
        # first tag URL: requesting GitHub's release page itself costs half
        # a second.
        $current = $Uri
        for ($hop = 0; $null -ne $script:AetherCurl -and $hop -lt 20; $hop++) {
            $result = Invoke-AetherCurl $current @('-fsSI', '--ssl-revoke-best-effort', '-o', 'NUL', '-w', '%{redirect_url}')
            if ($result.ExitCode -eq 22) {
                throw $result.Stderr
            }
            if ($result.ExitCode -ne 0) {
                $script:AetherCurl = $null
                break
            }
            if ([string]::IsNullOrEmpty($result.Stdout)) {
                $effective = $current
                break
            }
            $current = $result.Stdout
            Assert-AetherHttpsUri $current
            if (([Uri]$current).AbsolutePath -match '/tag/[^/]+/?$') {
                $effective = $current
                break
            }
        }
        if ([string]::IsNullOrWhiteSpace($effective)) {
            $ProgressPreference = 'SilentlyContinue'
            $response = Invoke-WebRequest -Uri $Uri -Method Head -UseBasicParsing -MaximumRedirection 20 -ErrorAction Stop
            $effective = if ($PSVersionTable.PSEdition -eq 'Desktop') {
                $response.BaseResponse.ResponseUri.AbsoluteUri
            } else {
                $response.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
            }
        }
        if ([string]::IsNullOrWhiteSpace($effective)) {
            throw 'the response did not expose its final release URL'
        }
        Assert-AetherHttpsUri $effective
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

function Assert-AetherHttpsUri {
    param([Parameter(Mandatory = $true)][string] $Uri)
    $parsed = $null
    if (-not [Uri]::TryCreate($Uri, [UriKind]::Absolute, [ref]$parsed)) {
        throw ("not an absolute URL: {0}" -f $Uri)
    }
    # Plain http is accepted only on loopback, where install-test.ps1 serves
    # its fixture releases.
    if ($parsed.Scheme -ne 'https' -and -not ($parsed.Scheme -eq 'http' -and $parsed.IsLoopback)) {
        throw ("refusing {0}: releases are downloaded over https only" -f $Uri)
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
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)] $ErrorRecord, $Status)
    if ($null -eq $Status) {
        return ("cannot reach {0}: {1}" -f $Uri, $ErrorRecord.Exception.Message)
    }
    $message = $ErrorRecord.Exception.Message
    if ($null -ne $ErrorRecord.ErrorDetails -and -not [string]::IsNullOrWhiteSpace($ErrorRecord.ErrorDetails.Message)) {
        $message = $ErrorRecord.ErrorDetails.Message
    }
    return ("{0} returned HTTP {1}: {2}" -f $Uri, $Status, $message)
}

function Receive-AetherWebFile {
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)][string] $Path)
    # Windows PowerShell's progress bar slows Invoke-WebRequest by an order
    # of magnitude; this local preference ends with the function.
    $ProgressPreference = 'SilentlyContinue'
    for ($attempt = 1; ; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -MaximumRedirection 20 -OutFile $Path -PassThru -ErrorAction Stop
        }
        catch {
            $status = Get-AetherHttpStatus $_.Exception
            if ($status -eq 404) { return $false }
            $retryable = $null -eq $status -or $status -eq 408 -or $status -ge 500 -or
                ($status -eq 429 -and -not (Test-AetherRateLimited $_.Exception $status))
            if (-not $retryable -or $attempt -ge 3) {
                throw (Format-AetherRequestError $Uri $_ $status)
            }
            Start-Sleep -Seconds 2
            continue
        }
        # Windows PowerShell follows a redirect from https to http; refuse
        # what such a redirect delivered.
        $final = if ($PSVersionTable.PSEdition -eq 'Desktop') {
            $response.BaseResponse.ResponseUri.AbsoluteUri
        } else {
            $response.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
        }
        try {
            Assert-AetherHttpsUri $final
        }
        catch {
            Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
            throw ("{0} redirected to {1}: {2}" -f $Uri, $final, $_.Exception.Message)
        }
        return $true
    }
}

function Receive-AetherFileOnce {
    param([Parameter(Mandatory = $true)][string] $Uri, [Parameter(Mandatory = $true)][string] $Path)
    if ($null -eq $script:AetherCurl) {
        return Receive-AetherWebFile $Uri $Path
    }
    $result = Invoke-AetherCurl $Uri @('-fsSL', '--retry', '3', '--ssl-revoke-best-effort', '-o', $Path, '-w', '%{http_code}')
    if ($result.ExitCode -eq 0) { return $true }
    if ($result.ExitCode -eq 22) {
        if ($result.Stdout -eq '404') { return $false }
        throw ("{0} returned HTTP {1}: {2}" -f $Uri, $result.Stdout, $result.Stderr)
    }
    # No HTTP answer: a system proxy curl.exe does not read, or a curl.exe
    # older than --ssl-revoke-best-effort. Invoke-WebRequest handles both.
    $curlError = 'curl.exe exited {0}: {1}' -f $result.ExitCode, $result.Stderr
    $script:AetherCurl = $null
    try {
        return Receive-AetherWebFile $Uri $Path
    }
    catch {
        throw ("{0} (before that, {1})" -f $_.Exception.Message, $curlError)
    }
}

function Receive-AetherFile {
    param(
        [Parameter(Mandatory = $true)][string] $Uri,
        [Parameter(Mandatory = $true)][string] $Path,
        [int] $NotFoundAttempts = 1
    )
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    for ($attempt = 1; ; $attempt++) {
        if (Receive-AetherFileOnce $Uri $Path) {
            $size = (Get-Item -LiteralPath $Path -ErrorAction Stop).Length
            Write-AetherDetail ('{0,-34} {1,9}  {2}' -f [IO.Path]::GetFileName($Path), (Format-AetherSize $size), (Format-AetherSeconds $watch))
            return $true
        }
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        if ($attempt -ge $NotFoundAttempts) { return $false }
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
        [Parameter(Mandatory = $true)][hashtable] $Wanted,
        [Parameter(Mandatory = $true)][string] $Directory,
        [Parameter(Mandatory = $true)][hashtable] $Release
    )
    $asset = $Wanted.Cli
    $Release.Tag = $Tag
    $Release.Ready = $false
    $Release.Missing = $null
    $Release.Reason = $null
    $Release.Unchanged = $false
    New-Item -ItemType Directory -Path $Directory -Force -ErrorAction Stop | Out-Null
    Write-AetherDetail ('{0}/{1}/' -f $BaseUrl, $Tag)
    if (-not (Receive-AetherFile ('{0}/{1}/checksums.txt' -f $BaseUrl, $Tag) (Join-Path $Directory 'checksums.txt'))) {
        $Release.Missing = 'checksums.txt'
        $Release.Reason = 'no checksums.txt'
        return
    }
    $present = @{}
    foreach ($name in @('aether-release.json', 'aether-release.json.sig')) {
        $present[$name] = Receive-AetherFile ('{0}/{1}/{2}' -f $BaseUrl, $Tag, $name) (Join-Path $Directory $name)
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
    $expected = Get-AetherChecksum (Join-Path $Directory 'checksums.txt') $asset
    if ($null -eq $expected) {
        if ($signed) {
            $manifest = [System.IO.File]::ReadAllText((Join-Path $Directory 'aether-release.json')) | ConvertFrom-Json -ErrorAction Stop
            if ($null -ne $manifest.assets -and $null -ne $manifest.assets.PSObject.Properties[$asset]) {
                throw ("aether-release.json of {0} lists {1} but checksums.txt does not; not installing it" -f $Tag, $asset)
            }
        }
        $Release.Reason = 'checksums.txt does not list ' + $asset
        return
    }
    $binaryPath = Join-Path $Directory $asset
    if ((Test-Path -LiteralPath $Wanted.Installed -PathType Leaf) -and
        (Get-FileHash -LiteralPath $Wanted.Installed -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant() -ceq $expected) {
        $binaryPath = $Wanted.Installed
        $Release.Unchanged = $true
        Write-AetherDetail ('{0}  not downloaded: {1} has the same SHA-256' -f $asset, $Wanted.Installed)
    }
    # checksums.txt already lists the binary, so a 404 may be CDN propagation.
    elseif (-not (Receive-AetherFile ('{0}/{1}/{2}' -f $BaseUrl, $Tag, $asset) $binaryPath 3)) {
        $Release.Missing = $asset
        $Release.Reason = 'no ' + $asset
        return
    }
    else {
        $actual = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()
        if ($actual -cne $expected) {
            Remove-Item -LiteralPath $binaryPath -Force -ErrorAction SilentlyContinue
            throw ("checksum mismatch for {0} of {1}: checksums.txt lists {2}, the download has {3}. The download was deleted and nothing was installed. Rerun the installer; if it fails again, something between you and {4} (a proxy or mirror) is changing the file - report it at https://github.com/3xDevOps/Aether/issues" -f $asset, $Tag, $expected, $actual, $BaseUrl)
        }
    }
    if ($signed) {
        Assert-AetherManifest (Join-Path $Directory 'aether-release.json') $Tag $asset $expected $binaryPath
        Write-AetherDetail ('verified {0} SHA-256 and size against checksums.txt and aether-release.json; aether-release.json.sig is not checked (PowerShell has no ed25519 verifier)' -f $asset)
    }
    else {
        Write-AetherDetail ('verified {0} SHA-256 against checksums.txt only: {1} predates release signing and has no aether-release.json' -f $asset, $Tag)
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
        throw ("cannot list releases: {0}" -f (Format-AetherRequestError $Uri $_ $status))
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
        [Parameter(Mandatory = $true)][hashtable] $Wanted,
        [Parameter(Mandatory = $true)][string] $Architecture,
        [Parameter(Mandatory = $true)][string] $Role,
        [Parameter(Mandatory = $true)][string] $TempRoot,
        [Parameter(Mandatory = $true)][hashtable] $Release
    )
    $latest = Resolve-AetherLatestVersion $LatestUrl
    Assert-AetherReleaseTagForUri $latest
    Assert-AetherTag $latest $Role
    Write-AetherDetail ('latest release: {0}' -f $latest)
    Read-AetherRelease $BaseUrl $latest $Wanted (Join-Path $TempRoot $latest) $Release
    if ($Release.Ready) { return }
    $latestReason = $Release.Reason
    $latestSemVer = Get-AetherSemVer $latest

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
    Write-AetherDetail ('{0} is not fully published yet ({1}); looking for the newest complete release' -f $latest, $latestReason)
    foreach ($candidate in $candidates) {
        $assets = $candidate.Assets
        # A fallback is always older than latest and always signed, so neither
        # a newer tag nor a 404 on every signature can pick it.
        if ($null -eq $latestSemVer -or (Compare-AetherSemVer $candidate.SemVer $latestSemVer) -ge 0 -or
            $assets -cnotcontains 'checksums.txt' -or $assets -cnotcontains $Wanted.Cli -or
            $assets -cnotcontains 'aether-release.json' -or $assets -cnotcontains 'aether-release.json.sig') {
            continue
        }
        try {
            Assert-AetherTag $candidate.Tag $Role
        }
        catch {
            continue
        }
        Read-AetherRelease $BaseUrl $candidate.Tag $Wanted (Join-Path $TempRoot $candidate.Tag) $Release
        if ($Release.Ready) {
            Write-AetherDetail ('installing {0}, the newest complete release' -f $candidate.Tag)
            return
        }
        Write-AetherDetail ('skipping {0}: not fully published ({1})' -f $candidate.Tag, $Release.Reason)
    }
    throw ("no complete release for windows/{0} among the newest {1} releases of {2}; pass -Version <tag>" -f $Architecture, $releases.Count, $Repo)
}

function Get-AetherRunningProcess {
    param([Parameter(Mandatory = $true)][string[]] $Paths)
    foreach ($process in Get-Process -Name 'aether', 'aether-desktop' -ErrorAction SilentlyContinue) {
        $path = $null
        try { $path = $process.Path } catch { continue }
        if ([string]::IsNullOrEmpty($path)) { continue }
        foreach ($wanted in $Paths) {
            if ([System.StringComparer]::OrdinalIgnoreCase.Equals($path, $wanted)) {
                $process
                break
            }
        }
    }
}

function Format-AetherRunningMessage {
    param([Parameter(Mandatory = $true)][string] $Problem, [AllowEmptyCollection()] $Processes)
    $lines = @($Problem)
    $Processes = @($Processes)
    if ($Processes.Count -gt 0) {
        $lines += 'Aether is running from the install directories:'
        foreach ($process in $Processes) {
            $lines += ('  {0} (pid {1}) {2}' -f $process.ProcessName, $process.Id, $process.Path)
        }
    }
    $lines += 'Nothing was changed.'
    $daemon = $null
    if (@($Processes | Where-Object { $_.ProcessName -eq 'aether' }).Count -gt 0) {
        try { $daemon = Get-ScheduledTask -TaskName 'aether-daemon' -ErrorAction SilentlyContinue } catch { }
    }
    if ($null -ne $daemon) {
        $lines += 'The sync daemon runs as the Scheduled Task aether-daemon, which restarts it within a minute of Stop-Process. End it first:'
        $lines += '  schtasks /End /TN aether-daemon'
    }
    if ($Processes.Count -gt 0) {
        $lines += 'Close Aether, or stop it with:'
        $lines += ('  Stop-Process -Id {0}' -f (($Processes | ForEach-Object { $_.Id }) -join ','))
    }
    $lines += ('Then rerun: {0}' -f $script:AetherRerun)
    if ($null -ne $daemon) {
        $lines += 'The sync daemon starts again at your next sign-in, or now with: schtasks /Run /TN aether-daemon'
    }
    return ($lines -join [Environment]::NewLine)
}

function Install-AetherCli {
    param(
        [Parameter(Mandatory = $true)][string] $DownloadedPath,
        [Parameter(Mandatory = $true)][string] $Directory,
        [Parameter(Mandatory = $true)][string] $ExpectedHash,
        [Parameter(Mandatory = $true)][string[]] $RunningPaths
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
        $cause = $_.Exception.GetBaseException()
        $problem = 'cannot replace {0}: {1}' -f $destination, $cause.Message
        $running = @(Get-AetherRunningProcess $RunningPaths)
        # ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION and
        # ERROR_UNABLE_TO_REMOVE_REPLACED leave the installed CLI untouched.
        if ($running.Count -gt 0 -or @(32, 33, 1175) -contains ($cause.HResult -band 0xFFFF)) {
            throw (Format-AetherRunningMessage $problem $running)
        }
        throw $problem
    }
    finally {
        if (Test-Path -LiteralPath $stage) {
            Remove-Item -LiteralPath $stage -Force -ErrorAction SilentlyContinue
        }
    }
    return $destination
}

function Get-AetherInstalledVersion {
    param([Parameter(Mandatory = $true)][string] $CliPath)
    if (-not (Test-Path -LiteralPath $CliPath -PathType Leaf)) { return $null }
    # Release builds carry their tag in the VERSIONINFO Comments field; the
    # numeric ProductVersion drops the pre-release suffix.
    try {
        $tag = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($CliPath).Comments
    }
    catch {
        return $null
    }
    if ([string]::IsNullOrWhiteSpace($tag) -or $null -eq (Get-AetherSemVer $tag.Trim())) { return $null }
    return $tag.Trim()
}

function Invoke-AetherGuiBuild {
    param([Parameter(Mandatory = $true)][string] $CliPath, [Parameter(Mandatory = $true)][string] $LogPath)
    $hadAetherBin = Test-Path -LiteralPath 'Env:AETHER_BIN'
    $oldAetherBin = [Environment]::GetEnvironmentVariable('AETHER_BIN', 'Process')
    $rerun = "& '{0}' gui build" -f $CliPath.Replace("'", "''")
    $log = $null
    try {
        [Environment]::SetEnvironmentVariable('AETHER_BIN', $CliPath, 'Process')
        New-Item -ItemType Directory -Path (Split-Path -Parent $LogPath) -Force -ErrorAction Stop | Out-Null
        $log = [System.IO.File]::Create($LogPath)
        $start = New-Object System.Diagnostics.ProcessStartInfo
        $start.FileName = $CliPath
        $start.Arguments = 'gui build --json'
        $start.UseShellExecute = $false
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        $start.StandardOutputEncoding = New-Object System.Text.UTF8Encoding $false
        try {
            $process = [System.Diagnostics.Process]::Start($start)
        }
        catch {
            throw ("aether gui build could not start: {0}. The CLI is installed; rerun: {1}" -f $_.Exception.Message, $rerun)
        }
        # The build's own output (npm, electron-builder, notes) is stderr; it
        # goes to the log so the console shows only the --json phases.
        $copy = $process.StandardError.BaseStream.CopyToAsync($log)
        $phase = $null
        $watch = $null
        $prebuilt = $false
        $failure = $null
        $installed = $null
        while ($null -ne ($line = $process.StandardOutput.ReadLine())) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $record = $null
            try { $record = ConvertFrom-Json -InputObject $line -ErrorAction Stop } catch { }
            $name = if ($null -ne $record -and $null -ne $record.phase) { [string]$record.phase } else { $line }
            if ($null -ne $phase) {
                $outcome = if ($name -eq 'error') { 'failed after ' } else { '' }
                Write-Host ($outcome + (Format-AetherSeconds $watch))
                $phase = $null
            }
            if ($name -eq 'done') {
                $installed = [string]$record.path
            }
            elseif ($name -eq 'error') {
                $failure = [string]$record.error
            }
            else {
                if ($name -eq 'downloading') { $prebuilt = $true }
                Write-AetherDetail ('{0,-26} ' -f $name) -NoNewline
                $phase = $name
                $watch = [System.Diagnostics.Stopwatch]::StartNew()
            }
        }
        $process.WaitForExit()
        $copy.Wait()
        $log.Dispose()
        $log = $null
        if ($null -ne $phase) {
            Write-Host ('stopped after ' + (Format-AetherSeconds $watch))
        }
        if ($process.ExitCode -ne 0 -or $null -ne $failure) {
            if ([string]::IsNullOrEmpty($failure)) {
                $failure = 'aether gui build exited with code {0}' -f $process.ExitCode
            }
            $tail = @(Get-Content -LiteralPath $LogPath -Tail 20 -Encoding UTF8 -ErrorAction SilentlyContinue | ForEach-Object { '  ' + $_ })
            $lines = @(('the desktop app was not installed: {0}' -f $failure))
            if ($tail.Count -gt 0) {
                $lines += ('last {0} lines of {1}:' -f $tail.Count, $LogPath)
                $lines += $tail
            }
            $lines += ('The CLI is installed; rerun: {0}' -f $rerun)
            throw ($lines -join [Environment]::NewLine)
        }
        $verb = if ($prebuilt) { 'installed' } else { 'built and installed' }
        if ([string]::IsNullOrEmpty($installed)) {
            Write-AetherDetail ('{0} the desktop app' -f $verb)
        }
        else {
            Write-AetherDetail ('{0} {1}' -f $verb, $installed)
        }
    }
    finally {
        if ($null -ne $log) { $log.Dispose() }
        if ($hadAetherBin) {
            [Environment]::SetEnvironmentVariable('AETHER_BIN', $oldAetherBin, 'Process')
        }
        else {
            Remove-Item -LiteralPath 'Env:AETHER_BIN' -Force -ErrorAction SilentlyContinue
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
    $changed = $newUserPath -cne $userPath
    if ($changed) {
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
        # Explorer rereads the environment only on WM_SETTINGCHANGE. .NET
        # broadcasts it (SendMessageTimeout, HWND_BROADCAST, "Environment")
        # after any user-scope change; deleting an unset name changes nothing
        # else and leaves the Path value kind written above intact.
        [Environment]::SetEnvironmentVariable('AETHER_INSTALL_UNSET', $null, 'User')
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
    return $changed
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
    $total = [System.Diagnostics.Stopwatch]::StartNew()
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'this installer supports Windows only'
    }
    if ($PSVersionTable.PSEdition -eq 'Desktop') {
        # .NET Framework may default to TLS 1.0, which GitHub refuses.
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    }
    $script:AetherCurl = Join-Path ([Environment]::GetEnvironmentVariable('SystemRoot', 'Process')) 'System32\curl.exe'
    if (-not (Test-Path -LiteralPath $script:AetherCurl -PathType Leaf)) { $script:AetherCurl = $null }
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
    foreach ($uri in @($baseUrl, $latestUrl, $apiUrl)) { Assert-AetherHttpsUri $uri }

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

    $wanted = @{
        Cli = 'aether-windows-{0}.exe' -f $architecture
        Installed = Join-Path $binDir 'aether.exe'
    }
    $destination = $wanted.Installed
    $desktopPath = Join-Path $desktopDir 'aether-desktop.exe'
    $script:AetherStep = 0
    $script:AetherSteps = if ($role -eq 'client') { 3 } else { 2 }
    $scope = if ($role -eq 'client') { 'CLI and desktop app' } else { 'CLI only' }
    Write-Host ('aether install: windows/{0}, {1}' -f $architecture, $scope)
    $tempRoot = $null
    try {
        $tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('.aether-install-{0}' -f ([Guid]::NewGuid().ToString('N')))
        New-Item -ItemType Directory -Path $tempRoot -Force -ErrorAction Stop | Out-Null
        $release = @{}
        if ($pinned) {
            Write-AetherStep ('downloading {0}' -f $version)
            Read-AetherRelease $baseUrl $version $wanted (Join-Path $tempRoot $version) $release
            if ($null -ne $release.Missing) {
                throw ("{0} has no published {1}; it may still be building or may have failed - not installing a different version" -f $version, $release.Missing)
            }
            if (-not $release.Ready) {
                throw ("{0} is incomplete ({1}); it may still be building or may have failed - not installing a different version" -f $version, $release.Reason)
            }
        }
        else {
            Write-AetherStep 'downloading the newest complete release'
            Find-AetherCompleteRelease $latestUrl $apiUrl $repo $baseUrl $wanted $architecture $role $tempRoot $release
        }

        $runningPaths = @()
        if (-not $release.Unchanged) { $runningPaths += $destination }
        if ($role -eq 'client') { $runningPaths += $desktopPath }
        if ($runningPaths.Count -gt 0) {
            $running = @(Get-AetherRunningProcess $runningPaths)
            if ($running.Count -gt 0) {
                throw (Format-AetherRunningMessage 'Aether must be closed to replace it.' $running)
            }
        }
        if ($release.Unchanged) {
            Write-AetherStep ('CLI {0} is already installed' -f $release.Tag)
        }
        else {
            $previous = Get-AetherInstalledVersion $destination
            if ($null -ne $previous) {
                Write-AetherStep ('installing the CLI: {0} -> {1}' -f $previous, $release.Tag)
            }
            else {
                Write-AetherStep ('installing the CLI {0}' -f $release.Tag)
            }
            $destination = Install-AetherCli $release.Path $binDir $release.Expected @($destination, $desktopPath)
        }
        Write-AetherDetail $destination
        try {
            $pathChanged = Update-AetherPath $binDir
        } catch {
            throw ("CLI installed at {0}, but could not update the user PATH: {1}" -f $destination, $_.Exception.Message)
        }
        if ($pathChanged) {
            Write-AetherDetail ('added {0} to the user PATH; terminals opened from now on find aether' -f $binDir)
        }
        Warn-AetherShadowing $binDir $destination

        if ($role -eq 'client') {
            $logPath = Join-Path $localAppData 'aether\install-desktop.log'
            Write-AetherStep ('desktop app (log: {0})' -f $logPath)
            Invoke-AetherGuiBuild $destination $logPath
        }
        Write-Host 'next:'
        if ($role -eq 'client') {
            Write-Host '  open Aether from the Start menu, or link from a new terminal:'
        }
        Write-Host '  aether link <server-host>:2222'
        Write-AetherStatus 'done:' Green ('Aether {0} installed in {1}' -f $release.Tag, (Format-AetherSeconds $total))
    }
    finally {
        if ($null -ne $tempRoot -and (Test-Path -LiteralPath $tempRoot)) {
            Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

$script:AetherRerun = 'the install command you ran'
if (-not [string]::IsNullOrEmpty($PSCommandPath)) {
    $parts = @("& '{0}'" -f $PSCommandPath.Replace("'", "''"))
    foreach ($name in @('Version', 'BinDir', 'Role')) {
        if ($PSBoundParameters.ContainsKey($name)) {
            $parts += ("-{0} '{1}'" -f $name, ([string]$PSBoundParameters[$name]).Replace("'", "''"))
        }
    }
    $script:AetherRerun = $parts -join ' '
}

try {
    Invoke-AetherInstall $Version $BinDir $Role
}
catch {
    [Console]::Error.WriteLine(("aether install: error: {0}" -f $_.Exception.Message))
    # Run from a file (-File, & or dot-sourced), exit ends only this script
    # and sets $LASTEXITCODE, so PowerShell prints no second error record.
    # Content run by Invoke-Expression has no script to exit; rethrow.
    if (-not [string]::IsNullOrEmpty($PSCommandPath)) {
        exit 1
    }
    throw
}
