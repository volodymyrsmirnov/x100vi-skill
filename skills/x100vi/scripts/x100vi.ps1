# Runs the x100vi tool on Windows, installing it on first use.
#
#   powershell -ExecutionPolicy Bypass -File x100vi.ps1 doctor --json
#   powershell -ExecutionPolicy Bypass -File x100vi.ps1 --self-update
#
# The tool is taken from PATH when it is already installed. Otherwise it is installed once
# with uv (or pipx). Set X100VI_SOURCE to install from somewhere else, e.g. a local checkout.
$ErrorActionPreference = "Stop"
$Source = if ($env:X100VI_SOURCE) { $env:X100VI_SOURCE } else { "git+https://github.com/volodymyrsmirnov/x100vi-skill" }

function Find-Tool {
    $cmd = Get-Command x100vi -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    if (Get-Command uv -ErrorAction SilentlyContinue) {
        $bin = (& uv tool dir --bin 2>$null)
        if ($bin) {
            $candidate = Join-Path $bin "x100vi.exe"
            if (Test-Path $candidate) { return $candidate }
        }
    }
    return $null
}

function Install-Tool([bool]$Update) {
    # Installer output goes to the error stream so standard output stays clean for --json.
    if (Get-Command uv -ErrorAction SilentlyContinue) {
        [Console]::Error.WriteLine("x100vi: installing the x100vi tool with uv from $Source ...")
        if ($Update) { & uv tool install --force --reinstall $Source 1>&2 } else { & uv tool install $Source 1>&2 }
    } elseif (Get-Command pipx -ErrorAction SilentlyContinue) {
        [Console]::Error.WriteLine("x100vi: installing the x100vi tool with pipx from $Source ...")
        if ($Update) { & pipx install --force $Source 1>&2 } else { & pipx install $Source 1>&2 }
    } else {
        [Console]::Error.WriteLine("error: the x100vi tool is not installed, and neither uv nor pipx is available to install it")
        [Console]::Error.WriteLine("hint: Install uv from https://docs.astral.sh/uv/getting-started/installation/ and run this again, or install the tool yourself: pipx install $Source")
        exit 127
    }
}

if ($args.Count -gt 0 -and $args[0] -eq "--self-update") {
    Install-Tool $true
    $tool = Find-Tool
    if (-not $tool) { [Console]::Error.WriteLine("error: the tool was installed but cannot be found"); exit 127 }
    & $tool --version
    exit $LASTEXITCODE
}

$tool = Find-Tool
if (-not $tool) {
    Install-Tool $false
    $tool = Find-Tool
    if (-not $tool) {
        [Console]::Error.WriteLine("error: the tool was installed but cannot be found")
        [Console]::Error.WriteLine("hint: add uv's tool folder to PATH with: uv tool update-shell")
        exit 127
    }
}
& $tool @args
exit $LASTEXITCODE
