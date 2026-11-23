# Runs the x100vi tool on Windows, downloading it on first use.
#
#   powershell -ExecutionPolicy Bypass -File x100vi.ps1 doctor --json
#   powershell -ExecutionPolicy Bypass -File x100vi.ps1 --self-update
#
# The tool is one binary per platform, published on every GitHub release. It is downloaded once
# (checksum verified) into %LOCALAPPDATA%\x100vi and reused. Environment:
#   X100VI_BIN          use this binary instead (e.g. one you built yourself)
#   X100VI_RELEASE_URL  download from here instead of the latest GitHub release
#   X100VI_CACHE        cache folder
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if ($env:X100VI_BIN) {
    & $env:X100VI_BIN @args
    exit $LASTEXITCODE
}

$ReleaseUrl = if ($env:X100VI_RELEASE_URL) { $env:X100VI_RELEASE_URL } else { "https://github.com/volodymyrsmirnov/x100vi-skill/releases/latest/download" }
$Asset = "x100vi-windows-amd64.exe"
$Cache = if ($env:X100VI_CACHE) { $env:X100VI_CACHE } elseif ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA "x100vi" } else { Join-Path $HOME ".x100vi" }
$Tool = Join-Path $Cache $Asset

function Download-Tool {
    # Progress goes to the error stream so standard output stays clean for --json.
    if ($env:CODEX_SANDBOX -or $env:CODEX_SANDBOX_NETWORK_DISABLED -eq "1" -or $env:X100VI_SANDBOX) {
        [Console]::Error.WriteLine("error: the x100vi tool is not downloaded yet and cannot be downloaded from inside the sandbox (no network there)")
        [Console]::Error.WriteLine("hint: Run this same command again outside the sandbox (ask for approval to run it unsandboxed). The camera also needs that: USB is not reachable from a sandbox.")
        exit 14
    }
    New-Item -ItemType Directory -Force -Path $Cache | Out-Null
    [Console]::Error.WriteLine("x100vi: downloading $Asset from $ReleaseUrl ...")
    $tmp = "$Tool.download"
    $sums = Join-Path $Cache "SHA256SUMS"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseUrl/$Asset" -OutFile $tmp
        Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseUrl/SHA256SUMS" -OutFile $sums
    } catch {
        [Console]::Error.WriteLine("error: could not download the x100vi tool from $ReleaseUrl ($($_.Exception.Message))")
        [Console]::Error.WriteLine("hint: Check the network connection and try again, or download $Asset from https://github.com/volodymyrsmirnov/x100vi-skill/releases and set X100VI_BIN to its path.")
        exit 127
    }
    $line = Get-Content $sums | Where-Object { $_ -match "\s\*?$([regex]::Escape($Asset))$" } | Select-Object -First 1
    $want = if ($line) { ($line -split '\s+')[0].ToLower() } else { "" }
    $got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
    Remove-Item -Force $sums
    if (-not $want -or $want -ne $got) {
        Remove-Item -Force $tmp
        [Console]::Error.WriteLine("error: the downloaded $Asset does not match the release checksum; it was not installed")
        exit 127
    }
    Move-Item -Force $tmp $Tool
}

if ($args.Count -gt 0 -and $args[0] -eq "--self-update") {
    Download-Tool
    & $Tool --version
    exit $LASTEXITCODE
}

if (-not (Test-Path $Tool)) {
    Download-Tool
}
& $Tool @args
exit $LASTEXITCODE
