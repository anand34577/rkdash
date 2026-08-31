@echo off
setlocal
rem Cross-compiles rkdash for linux/arm64 (RK3566, RK3576, RK3588 boards),
rem regardless of any GOOS/GOARCH already set in this shell's environment.

where go >nul 2>nul
if errorlevel 1 (
    echo [error] Go is not on PATH. Install it from https://go.dev/dl/ and re-run this script.
    exit /b 1
)

set "SCRIPT_DIR=%~dp0"
set "OUT_DIR=%SCRIPT_DIR%dist"
if not exist "%OUT_DIR%" mkdir "%OUT_DIR%"
set "OUT_FILE=%OUT_DIR%\rkdash-linux-arm64"

pushd "%SCRIPT_DIR%"

set GOOS=linux
set GOARCH=arm64
set CGO_ENABLED=0

for /f %%v in ('git describe --tags --always --dirty 2^>nul') do set "VER=%%v"
if not defined VER set "VER=dev"

echo Building %OUT_FILE% (GOOS=%GOOS% GOARCH=%GOARCH%, version=%VER%)...
go build -trimpath -ldflags "-s -w -X main.appVersion=%VER%" -o "%OUT_FILE%" .
set "BUILD_ERR=%ERRORLEVEL%"

popd

if not "%BUILD_ERR%"=="0" (
    echo [error] Build failed.
    exit /b %BUILD_ERR%
)

echo Done: %OUT_FILE%
echo Copy it to the board and run: chmod +x rkdash-linux-arm64 ^&^& sudo ./rkdash-linux-arm64
endlocal
