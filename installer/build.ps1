<#
.SYNOPSIS
  Builds hyperv-o11y-companion's two Windows Service binaries and packages
  them, plus their example configs, into a single MSI via the WiX v4
  toolset.

.PREREQUISITES
  - Go toolchain (any OS with GOOS=windows cross-compile support)
  - WiX v4 CLI: dotnet tool install --global wix
  - Run from the repo root, or pass -RepoRoot explicitly.
#>
param(
    [string]$RepoRoot = (Resolve-Path "$PSScriptRoot\.."),
    [string]$OutDir = "$PSScriptRoot\out"
)

$ErrorActionPreference = "Stop"

$staging = Join-Path $PSScriptRoot "staging"
$stagingBin = Join-Path $staging "bin"
$stagingConfig = Join-Path $staging "config"

Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $stagingBin, $stagingConfig, $OutDir | Out-Null

Write-Host "Building scvmm-poller.exe and host-companion.exe (GOOS=windows)..."
Push-Location $RepoRoot
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -o (Join-Path $stagingBin "scvmm-poller.exe") ./cmd/scvmm-poller
    go build -o (Join-Path $stagingBin "host-companion.exe") ./cmd/host-companion
} finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}

Copy-Item (Join-Path $RepoRoot "config\scvmm-poller.yaml") $stagingConfig
Copy-Item (Join-Path $RepoRoot "config\host-companion.yaml") $stagingConfig

Write-Host "Building MSI..."
wix build (Join-Path $PSScriptRoot "main.wxs") -o (Join-Path $OutDir "HyperVO11yCompanion.msi")

Write-Host "Done: $(Join-Path $OutDir 'HyperVO11yCompanion.msi')"
