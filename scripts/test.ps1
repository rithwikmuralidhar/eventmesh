# Go unit tests (with -race when a C toolchain exists) and frontend unit tests.
. "$PSScriptRoot\common.ps1"
$race = @()
if (Get-Command gcc -ErrorAction SilentlyContinue) { $env:CGO_ENABLED = '1'; $race = @('-race') }
else { Write-Warning '-race NOT RUN: no C compiler (gcc) on PATH. See docs/windows-setup.md.' }
Invoke-Native 'go' (@('test', '-count=1') + $race + @('./internal/...')) $script:Backend
if (Test-Path (Join-Path $script:Frontend 'package.json')) { Invoke-Native 'npm' @('test', '--', '--run') $script:Frontend }
