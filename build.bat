@echo off
setlocal

set BUILD_DIR=build

if not exist "internal\web\dist\index.html" (
    echo ERROR: internal\web\dist does not contain a frontend snapshot
    exit /b 1
)

if exist "%BUILD_DIR%" rmdir /s /q "%BUILD_DIR%"
mkdir "%BUILD_DIR%\release\windows" 2>nul

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o "%BUILD_DIR%\arcway-windows-amd64.exe" .\cmd\server
if errorlevel 1 exit /b 1

set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags="-s -w" -o "%BUILD_DIR%\arcway-expiry-guard-linux-amd64" .\cmd\arcway-expiry-guard
if errorlevel 1 exit /b 1
set GOARCH=arm64
go build -trimpath -ldflags="-s -w" -o "%BUILD_DIR%\arcway-expiry-guard-linux-arm64" .\cmd\arcway-expiry-guard
if errorlevel 1 exit /b 1

copy "%BUILD_DIR%\arcway-windows-amd64.exe" "%BUILD_DIR%\release\windows\" >nul
copy "%BUILD_DIR%\arcway-expiry-guard-linux-amd64" "%BUILD_DIR%\release\windows\" >nul
copy "%BUILD_DIR%\arcway-expiry-guard-linux-arm64" "%BUILD_DIR%\release\windows\" >nul
echo Build complete: %BUILD_DIR%\arcway-windows-amd64.exe
