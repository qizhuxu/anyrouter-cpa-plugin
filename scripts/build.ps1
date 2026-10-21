# Build the anyrouter CPA plugin on Windows and prove the result is loadable.
#
#   pwsh -File scripts/build.ps1                    # build + load test
#   pwsh -File scripts/build.ps1 -Install <dir>     # build + copy into <plugins dir>
#   pwsh -File scripts/build.ps1 -All               # also vet + unit tests
#
# Why the load test matters: a MinGW build of a Go c-shared library can link into
# a PE image Windows rejects ("%1 is not a valid Win32 application"), and the
# failure only shows up when CPA tries to load the plugin. TDM-GCC 10.3.0 does
# exactly that on this machine; MinGW-W64 11.3.0 (D:/Code/mingw64) does not.

[CmdletBinding()]
param(
    [string]$OutputDir = "dist",
    [string]$Install,
    [switch]$All,
    [string[]]$Toolchain = @()
)

$ErrorActionPreference = "Stop"
$pluginId = "anyrouter"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if ($Toolchain.Count -eq 0) {
    $Toolchain = @(
        $env:CC,
        "D:\Code\mingw64\bin\gcc.exe",
        "C:\mingw64\bin\gcc.exe",
        "D:\msys64\mingw64\bin\gcc.exe"
    ) | Where-Object { $_ -and (Test-Path $_) }
    if ($Toolchain.Count -eq 0) {
        $onPath = Get-Command gcc -ErrorAction SilentlyContinue
        if ($onPath) { $Toolchain = @($onPath.Source) }
    }
}

if ($Toolchain.Count -eq 0) {
    throw "no C toolchain found; install MinGW-W64 and pass -Toolchain <gcc path>"
}

$env:CGO_ENABLED = "1"
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn" }

if ($All) {
    Write-Host "== go vet ==" -ForegroundColor Cyan
    $env:CGO_ENABLED = "0"
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
    Write-Host "== go test ==" -ForegroundColor Cyan
    & go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw "go test failed" }
    $env:CGO_ENABLED = "1"
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$target = Join-Path $OutputDir "$pluginId.dll"
$built = $false
$lastError = ""

foreach ($cc in $Toolchain) {
    Write-Host "== building with $cc ==" -ForegroundColor Cyan
    $env:CC = $cc
    Remove-Item $target -ErrorAction SilentlyContinue
    & go build -buildmode=c-shared -trimpath -ldflags "-s -w" -o $target .
    if ($LASTEXITCODE -ne 0) {
        $lastError = "go build failed with $cc"
        continue
    }
    $probe = python -c "import ctypes,sys; ctypes.WinDLL(sys.argv[1]); print('LOAD OK')" $target 2>&1
    if ($LASTEXITCODE -eq 0 -and "$probe" -match "LOAD OK") {
        Write-Host "   $probe" -ForegroundColor Green
        $built = $true
        break
    }
    $lastError = "toolchain $cc produced an unloadable library: $probe"
    Write-Warning $lastError
}

if (-not $built) {
    throw $lastError
}

$dll = Get-Item $target
Write-Host ("== built {0} ({1:N0} bytes) ==" -f $dll.FullName, $dll.Length) -ForegroundColor Green

if ($Install) {
    New-Item -ItemType Directory -Force -Path $Install | Out-Null
    Copy-Item $target (Join-Path $Install "$pluginId.dll") -Force
    Write-Host "== installed to $(Join-Path $Install "$pluginId.dll") ==" -ForegroundColor Green
    Write-Host "   remember: plugins.enabled must be true, plugins.configs.$pluginId.enabled must be true," -ForegroundColor Yellow
    Write-Host "   and a running CPA instance keeps the old shadow copy until it is reloaded or restarted." -ForegroundColor Yellow
}
