[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string] $Tag,

    [Parameter(Position = 1)]
    [string] $OutputDirectory,

    [switch] $Offline
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $repoRoot "dist/$Tag"
}
if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path (Get-Location) $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

function Invoke-Checked {
    param([string] $Name, [string[]] $Arguments)
    & $Name @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) "worm-release-$([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $tempRoot | Out-Null
$envNames = @('CGO_ENABLED', 'GOOS', 'GOARCH', 'GOPROXY')
$savedEnv = @{}
foreach ($name in $envNames) { $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    Push-Location $repoRoot
    if ($Offline) {
        Invoke-Checked npm @('ci', '--offline', '--prefix', 'web')
        $env:GOPROXY = 'off'
    } else {
        Invoke-Checked npm @('ci', '--prefix', 'web')
    }
    Invoke-Checked npm @('run', 'build', '--prefix', 'web')
    Invoke-Checked go @('vet', './...')
    Invoke-Checked go @('test', '-count=1', './...')

    $targets = @(
        @{ OS = 'linux'; Arch = 'amd64' },
        @{ OS = 'linux'; Arch = 'arm64' },
        @{ OS = 'darwin'; Arch = 'amd64' },
        @{ OS = 'darwin'; Arch = 'arm64' },
        @{ OS = 'windows'; Arch = 'amd64' },
        @{ OS = 'windows'; Arch = 'arm64' }
    )
    $archives = [Collections.Generic.List[string]]::new()
    foreach ($target in $targets) {
        $stage = Join-Path $tempRoot "$($target.OS)-$($target.Arch)"
        New-Item -ItemType Directory -Path $stage | Out-Null
        $binary = if ($target.OS -eq 'windows') { 'worm.exe' } else { 'worm' }
        $env:CGO_ENABLED = '0'
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        Invoke-Checked go @('build', '-trimpath', '-ldflags', "-s -w -X 'main.Version=$Tag'", '-o', (Join-Path $stage $binary), './cmd/worm')
        foreach ($directory in @('packs', 'schemas', 'sources', 'sinks')) {
            Copy-Item -Recurse (Join-Path $repoRoot $directory) $stage
        }

        $archive = "worm-$Tag-$($target.OS)-$($target.Arch).tar.gz"
        Invoke-Checked tar @('-czf', (Join-Path $OutputDirectory $archive), '-C', $stage, '.')
        $archives.Add($archive)
        Write-Host "Built $(Join-Path $OutputDirectory $archive)"
    }

    $lines = foreach ($archive in $archives) {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $OutputDirectory $archive)).Hash.ToLowerInvariant()
        "$hash  $archive"
    }
    Set-Content -LiteralPath (Join-Path $OutputDirectory 'SHA256SUMS') -Value $lines -Encoding ascii
    Write-Host "Checksums: $(Join-Path $OutputDirectory 'SHA256SUMS')"
} finally {
    if ((Get-Location).Path -eq $repoRoot) { Pop-Location }
    foreach ($name in $envNames) {
        [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], 'Process')
    }
    Remove-Item -LiteralPath $tempRoot -Recurse -Force
}
