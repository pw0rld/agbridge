# Windows CLI installer. Requires Windows PowerShell 5.1 or PowerShell 7+.
# Usage: .\install.ps1 [-Version v0.0.2] [-InstallDir C:\Tools\agbridge]
[CmdletBinding()]
param(
    [string]$Version = $env:VERSION,
    [string]$Repo = 'pw0rld/agbridge',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'agbridge\bin'),
    [switch]$NoPathUpdate
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$nativeArch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $nativeArch = $env:PROCESSOR_ARCHITEW6432 }
switch ($nativeArch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw "agbridge: unsupported Windows architecture: $nativeArch" }
}

if (-not $Version) {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -TimeoutSec 30
    $Version = $release.tag_name
    if (-not $Version) { throw 'agbridge: could not detect latest release; pass -Version' }
}

$asset = "agbridge-windows-$arch.exe"
$base = "https://github.com/$Repo/releases/download/$Version"
$tempDir = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempDir
try {
    $download = Join-Path $tempDir $asset
    Write-Host "agbridge: downloading $base/$asset"
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download -TimeoutSec 120
    $sums = Join-Path $tempDir 'SHA256SUMS'
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sums -TimeoutSec 30
    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($asset) + '$'
    $entries = @(Get-Content -LiteralPath $sums | Where-Object { $_ -match $pattern })
    if ($entries.Count -ne 1) { throw "agbridge: missing or ambiguous SHA-256 for $asset" }
    $null = $entries[0] -match $pattern
    $expected = $Matches[1]
    $actual = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash
    if ($actual -ne $expected) { throw 'agbridge: SHA-256 mismatch; binary was not installed' }

    $null = New-Item -ItemType Directory -Path $InstallDir -Force
    $InstallDir = (Resolve-Path -LiteralPath $InstallDir).Path
    $exe = Join-Path $InstallDir 'agbridge.exe'
    Copy-Item -LiteralPath $download -Destination $exe -Force

    if (-not $NoPathUpdate) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if (@($userPath -split ';') -notcontains $InstallDir) {
            $newPath = (@($userPath -split ';' | Where-Object { $_ }) + $InstallDir) -join ';'
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        }
        if (@($env:Path -split ';') -notcontains $InstallDir) {
            $env:Path = "$InstallDir;$env:Path"
        }
    }

    Write-Host "agbridge: SHA-256 verified; installed to $exe"
    & $exe --version
    if ($LASTEXITCODE -ne 0) { throw 'agbridge: installed binary could not run' }
    Write-Host 'agbridge: enroll with a bridge token, then run agbridge status.'
} finally {
    Remove-Item -LiteralPath $tempDir -Recurse -Force
}
