# Cross-compiles rkdash for linux/arm64 (RK3566, RK3576, RK3588 boards),
# regardless of any GOOS/GOARCH already set in this session.
$ErrorActionPreference = "Stop"

$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    Write-Error "Go is not on PATH. Install it from https://go.dev/dl/ and re-run this script."
    exit 1
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$outDir = Join-Path $scriptDir "dist"
if (-not (Test-Path $outDir)) {
    New-Item -ItemType Directory -Path $outDir | Out-Null
}
$outFile = Join-Path $outDir "rkdash-linux-arm64"

Push-Location $scriptDir
try {
    $env:GOOS = "linux"
    $env:GOARCH = "arm64"
    $env:CGO_ENABLED = "0"

    Write-Host "Building $outFile (GOOS=$($env:GOOS) GOARCH=$($env:GOARCH))..."
    & go build -trimpath -ldflags "-s -w" -o $outFile .
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }

    Write-Host "Done: $outFile"
    Write-Host "Copy it to the board and run: chmod +x rkdash-linux-arm64 && sudo ./rkdash-linux-arm64"
} finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
