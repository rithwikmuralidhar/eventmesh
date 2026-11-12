# Shared helpers, dot-sourced by every script: . "$PSScriptRoot\common.ps1"
$ErrorActionPreference = 'Stop'
$script:Root = Split-Path -Parent $PSScriptRoot
$script:Backend = Join-Path $script:Root 'backend'
$script:Frontend = Join-Path $script:Root 'frontend'

# Loads KEY=VALUE lines from .env into the process environment. Values already
# set in the environment win, so a caller can override a single variable.
function Import-DotEnv {
    $file = Join-Path $script:Root '.env'
    if (-not (Test-Path $file)) { return }
    foreach ($line in Get-Content $file) {
        $t = $line.Trim()
        if ($t -eq '' -or $t.StartsWith('#')) { continue }
        $i = $t.IndexOf('=')
        if ($i -lt 1) { continue }
        $name = $t.Substring(0, $i).Trim()
        $value = $t.Substring($i + 1).Trim().Trim('"')
        if (-not [Environment]::GetEnvironmentVariable($name, 'Process')) {
            [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        }
    }
}

function Assert-Command([string]$Name, [string]$Hint) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "'$Name' was not found on PATH. $Hint"
    }
}

# Runs a native command and stops the script if it exits non-zero.
function Invoke-Native([string]$File, [string[]]$Arguments, [string]$WorkingDirectory = $script:Root) {
    Push-Location $WorkingDirectory
    try {
        & $File @Arguments
        if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') failed with exit code $LASTEXITCODE" }
    } finally { Pop-Location }
}

Import-DotEnv
