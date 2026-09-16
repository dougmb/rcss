# Install the latest RCSS release on Windows.
#
#   irm https://raw.githubusercontent.com/dougmb/rcss/main/install.ps1 | iex
#
# Environment:
#   RCSS_VERSION      release tag to install (default: latest), e.g. v0.2.0
#   RCSS_INSTALL_DIR  where to put rcss.exe (default: %LOCALAPPDATA%\Programs\rcss)
$ErrorActionPreference = 'Stop'

$Repo = 'dougmb/rcss'
$InstallDir = if ($env:RCSS_INSTALL_DIR) { $env:RCSS_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\rcss' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "rcss install: unsupported architecture $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:RCSS_VERSION
if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name
}
if (-not $version) { throw 'rcss install: could not determine the latest release' }

$archive = "rcss_$($version.TrimStart('v'))_windows_$arch.zip"
$base = "https://github.com/$Repo/releases/download/$version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("rcss-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "Downloading rcss $version (windows/$arch)..."
    Invoke-WebRequest "$base/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive }
    if (-not $line) { throw "rcss install: $archive is not listed in checksums.txt" }
    $expected = ($line -split '\s+')[0]
    $actual = (Get-FileHash (Join-Path $tmp $archive) -Algorithm SHA256).Hash
    if ($expected -ne $actual.ToLower()) { throw "rcss install: checksum mismatch for $archive" }

    Expand-Archive (Join-Path $tmp $archive) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item (Join-Path $tmp 'rcss.exe') (Join-Path $InstallDir 'rcss.exe') -Force
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "Installed rcss $version to $InstallDir\rcss.exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $InstallDir) {
    [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';'), $InstallDir) -join ';').TrimStart(';'), 'User')
    Write-Host "Added $InstallDir to your user PATH; open a new terminal to use it."
}

if (-not (Get-Command rclone -ErrorAction SilentlyContinue)) {
    Write-Host ''
    Write-Host 'rclone was not found. RCSS needs it for every cloud operation:'
    Write-Host '  winget install Rclone.Rclone'
}

Write-Host ''
Write-Host "Run 'rcss' to open the app."
