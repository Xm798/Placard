# Install script for the Placard CLI on Windows.
#
# Usage:
#   irm https://<your placard server>/install.ps1 | iex
#
# The binaries come from the project's GitHub releases, which carry two tag
# namespaces: vX.Y.Z is the SERVER and cli/vX.Y.Z is the CLI. Everything below
# looks at cli/v only.
#
# Environment:
#   PLACARD_BIN_DIR        install directory (default: %LOCALAPPDATA%\placard\bin)
#   PLACARD_CLI_VERSION    pin a version instead of taking the newest release
#   PLACARD_SKIP_CHECKSUM  set to 1 to skip sha256 verification (prints a warning)
#
# Security note: checksums.txt is an asset of the same release as the binaries,
# so the sha256 check defends against transport corruption and against a
# silently skipped verification — not against tampering by someone holding
# write access to the repository's releases.

#Requires -Version 5.1
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 still negotiates TLS 1.0 by default on some hosts, and
# GitHub serves 1.2 and above only — without this every request below fails
# with an unhelpful "underlying connection was closed".
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# --- Configuration ---

$Repo = 'Xm798/placard'
$ApiUrl = "https://api.github.com/repos/$Repo/releases?per_page=100"
# Server (vX.Y.Z) and CLI (cli/vX.Y.Z) releases share one list, so a long run of
# server releases can push the newest CLI release onto a later page.
$MaxPages = 5
# A Placard server rewrites this line when it serves /install.ps1, pinning the
# CLI release it resolved. Left empty (installing straight from GitHub) the
# newest release wins. The server-side rewrite matches this line exactly.
$DefaultCliVersion = ''
$BinDir = if ($env:PLACARD_BIN_DIR) { $env:PLACARD_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'placard\bin' }
$TmpFile = $null

# --- Output helpers ---

function Write-Err($msg)  { Write-Host "error: $msg" -ForegroundColor Red }
function Write-Warn($msg) { Write-Host "warning: $msg" -ForegroundColor Yellow }
function Write-Info($msg) { Write-Host $msg -ForegroundColor DarkGray }
function Write-Bold($msg) { Write-Host $msg -ForegroundColor White }
function Write-Ok($msg)   { Write-Host $msg -ForegroundColor Green }

function Fail($msg) {
    Write-Err $msg
    exit 1
}

function Cleanup {
    if ($script:TmpFile -and (Test-Path $script:TmpFile)) {
        Remove-Item -Force -ErrorAction SilentlyContinue $script:TmpFile
    }
}

# --- Arch detection (x64 + arm64; the build matrix ships both) ---

function Get-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'x64' }
        'ARM64' { return 'arm64' }
        default { Fail "Unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
    }
}

# --- Release resolution ---

# Asset URLs are taken as GitHub reported them rather than assembled here,
# because the CLI's tags contain a slash (cli/v1.2.3) and how that is spelled
# inside a download URL is GitHub's business, not this script's.
function Get-ReleasePage($page) {
    try {
        return @(Invoke-RestMethod -UseBasicParsing -Uri "$ApiUrl&page=$page" -Headers @{ 'Accept' = 'application/vnd.github+json' })
    } catch {
        Fail "Failed to fetch ${ApiUrl}: $_"
    }
}

# Get-CliRelease returns the pinned release, or the newest stable one.
# Prereleases are skipped: an install must not land on a release candidate
# nobody asked for.
function Get-CliRelease($version) {
    Write-Info 'Fetching release list...'
    for ($page = 1; $page -le $MaxPages; $page++) {
        $releases = Get-ReleasePage $page
        if (-not $releases) { break }
        $cli = @($releases | Where-Object { $_.tag_name -like 'cli/v*' -and -not $_.draft })
        if ($version) {
            $found = $cli | Where-Object { $_.tag_name -eq "cli/v$version" } | Select-Object -First 1
        } else {
            $found = $cli | Where-Object { -not $_.prerelease } | Select-Object -First 1
        }
        if ($found) { return $found }
    }
    if ($version) { Fail "No release tagged cli/v$version exists at $ApiUrl" }
    Fail "No cli/v* release found at $ApiUrl"
}

function Get-AssetUrl($release, $name) {
    $asset = $release.assets | Where-Object { $_.name -eq $name } | Select-Object -First 1
    if (-not $asset) { return $null }
    return $asset.browser_download_url
}

# --- Checksum verification (fail-closed, mirrors install.sh) ---

function Test-Checksum($file, $binaryName, $checksumsUrl) {
    if ($env:PLACARD_SKIP_CHECKSUM -eq '1') {
        Write-Warn 'PLACARD_SKIP_CHECKSUM=1 - sha256 verification DISABLED for this install.'
        return
    }
    if (-not $checksumsUrl) {
        Fail 'The release publishes no checksums.txt - installation aborted. Set PLACARD_SKIP_CHECKSUM=1 to install without verification.'
    }

    Write-Info 'Verifying checksum...'
    try {
        $checksums = (Invoke-WebRequest -UseBasicParsing -Uri $checksumsUrl).Content
        if ($checksums -is [byte[]]) {
            $checksums = [System.Text.Encoding]::UTF8.GetString($checksums)
        }
    } catch {
        Fail "Could not fetch ${checksumsUrl} - installation aborted. Set PLACARD_SKIP_CHECKSUM=1 to install without verification. ($_)"
    }
    if (-not $checksums) {
        Fail "Empty checksums.txt at ${checksumsUrl} - installation aborted."
    }

    $expected = $null
    foreach ($line in $checksums -split "`n") {
        $line = $line.Trim()
        if (-not $line) { continue }
        $parts = $line -split '\s+', 2
        if ($parts.Length -eq 2 -and $parts[1] -eq $binaryName) {
            $expected = $parts[0].ToLower()
            break
        }
    }
    if (-not $expected) {
        Fail "No checksum entry for $binaryName in checksums.txt - installation aborted."
    }

    $actual = (Get-FileHash -Algorithm SHA256 -Path $file).Hash.ToLower()
    if ($actual -ne $expected) {
        Fail "SHA256 mismatch - installation aborted. Expected $expected, got $actual."
    }
    Write-Info 'Checksum verified.'
}

# --- PATH update ---

function Add-ToUserPath($dir) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    $entries = $userPath.Split(';') | Where-Object { $_ -ne '' }
    if ($entries -contains $dir) {
        return $false
    }
    $newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    return $true
}

# --- Main install logic ---

function Install-Placard {
    $arch = Get-Arch
    $pinned = if ($env:PLACARD_CLI_VERSION) { $env:PLACARD_CLI_VERSION } else { $DefaultCliVersion }
    $release = Get-CliRelease $pinned
    $version = $release.tag_name.Substring('cli/v'.Length)
    $binaryName = "placard-$version-windows-$arch.exe"
    $binaryUrl = Get-AssetUrl $release $binaryName
    if (-not $binaryUrl) {
        Fail "Release $($release.tag_name) has no asset named $binaryName (this platform is not published)."
    }

    Write-Bold "Installing placard $version (windows-$arch)..."
    Write-Host ''

    $script:TmpFile = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName() + '.exe')

    Write-Info "Downloading $binaryUrl..."
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $binaryUrl -OutFile $script:TmpFile
    } catch {
        Fail "Failed to download placard from ${binaryUrl}: $_"
    }

    Test-Checksum $script:TmpFile $binaryName (Get-AssetUrl $release 'checksums.txt')

    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    }

    # Windows cannot overwrite an executable that has an open handle, so an
    # existing install is renamed to placard.exe.old first. NTFS allows
    # renaming a file with open handles; that is what makes this work.
    $target = Join-Path $BinDir 'placard.exe'
    if (Test-Path $target) {
        Remove-Item -Force -ErrorAction SilentlyContinue "$target.old"
        try {
            Rename-Item -Path $target -NewName 'placard.exe.old' -ErrorAction Stop
        } catch {
            Fail "Could not replace existing $target (is it running in another process?): $_"
        }
    }
    Move-Item -Force -Path $script:TmpFile -Destination $target
    $script:TmpFile = $null

    try {
        & $target --version | Out-Null
    } catch {
        Fail "Verification failed: $target did not run. The binary may be corrupted."
    }

    Write-Host ''
    Write-Ok "placard $version installed successfully!"
    Write-Host ''
    & $target --version

    $added = Add-ToUserPath $BinDir
    Write-Host ''
    if ($added) {
        Write-Bold "Added $BinDir to your user PATH."
        Write-Info 'Restart your terminal (or sign out and back in) for the change to take effect.'
    } else {
        $sessionPath = $env:Path -split ';'
        if ($sessionPath -contains $BinDir) {
            Write-Bold 'placard is ready to use!'
            Write-Host '  placard --help' -ForegroundColor Cyan
        } else {
            Write-Bold "$BinDir is already in your user PATH."
            Write-Info 'Restart your terminal for the change to take effect.'
        }
    }
}

try {
    Install-Placard
} finally {
    Cleanup
}
