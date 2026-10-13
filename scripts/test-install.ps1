# Offline installer regression tests, run with Windows PowerShell 5.1.
param([Parameter(Mandatory = $true)][string]$Binary)
$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$testDir = Join-Path ([IO.Path]::GetTempPath()) ('agbridge-installer-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $testDir
$installer = Join-Path $PSScriptRoot 'install.ps1'
$installDir = Join-Path $testDir 'path with spaces'
$originalArch = $env:PROCESSOR_ARCHITECTURE
$originalNativeArch = $env:PROCESSOR_ARCHITEW6432
$originalPath = $env:Path
$originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$fixture = @{ ChecksumMode = 'valid'; RequestedAsset = '' }

function Invoke-RestMethod {
    param($Uri, $TimeoutSec)
    return @{ tag_name = 'v-test' }
}

function Invoke-WebRequest {
    param([switch]$UseBasicParsing, $Uri, $OutFile, $TimeoutSec)
    if ($Uri.EndsWith('/SHA256SUMS')) {
        $hash = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash
        $line = "$hash  $($fixture.RequestedAsset)"
        switch ($fixture.ChecksumMode) {
            'mismatch' { $line = ('0' * 64) + "  $($fixture.RequestedAsset)" }
            'missing' { $line = "$hash  another-binary.exe" }
            'duplicate' { $line = "$line`n$line" }
            'unavailable' { throw 'simulated download failure' }
        }
        Set-Content -LiteralPath $OutFile -Value $line -Encoding Ascii
    } else {
        $fixture.RequestedAsset = $Uri.Split('/')[-1]
        Copy-Item -LiteralPath $Binary -Destination $OutFile
    }
}

try {
    foreach ($arch in @('AMD64', 'ARM64')) {
        $env:PROCESSOR_ARCHITECTURE = $arch
        $env:PROCESSOR_ARCHITEW6432 = ''
        & $installer -Version 'v-test' -InstallDir $installDir -NoPathUpdate
        if ($fixture.RequestedAsset -ne "agbridge-windows-$($arch.ToLower()).exe") {
            throw "wrong asset: $($fixture.RequestedAsset)"
        }
        $installed = Join-Path $installDir 'agbridge.exe'
        if ((Get-FileHash $installed).Hash -ne (Get-FileHash $Binary).Hash) {
            throw 'installed binary differs'
        }
    }

    # A 32-bit shell on 64-bit Windows must select the native asset.
    $env:PROCESSOR_ARCHITECTURE = 'x86'
    $env:PROCESSOR_ARCHITEW6432 = 'AMD64'
    & $installer -Version '' -InstallDir $installDir -NoPathUpdate
    if ($fixture.RequestedAsset -ne 'agbridge-windows-amd64.exe') { throw 'wrong native architecture' }

    Set-Content -LiteralPath $installed -Value 'previous installation' -Encoding Ascii
    $previousHash = (Get-FileHash $installed).Hash
    foreach ($mode in @('mismatch', 'missing', 'duplicate', 'unavailable')) {
        $fixture.ChecksumMode = $mode
        $failed = $false
        try { & $installer -Version 'v-test' -InstallDir $installDir -NoPathUpdate }
        catch { $failed = $true }
        if (-not $failed) { throw "installer accepted $mode checksum" }
        if ((Get-FileHash $installed).Hash -ne $previousHash) {
            throw 'failed installation overwrote existing binary'
        }
    }
    if ($env:Path -ne $originalPath -or [Environment]::GetEnvironmentVariable('Path', 'User') -ne $originalUserPath) {
        throw '-NoPathUpdate changed PATH'
    }
    Write-Host 'Windows installer tests passed.'
} finally {
    $env:PROCESSOR_ARCHITECTURE = $originalArch
    $env:PROCESSOR_ARCHITEW6432 = $originalNativeArch
    Remove-Item -LiteralPath $testDir -Recurse -Force
}
