# gofmt, go vet, golangci-lint (if installed), and the frontend checks.
. "$PSScriptRoot\common.ps1"
$unformatted = & gofmt -l (Join-Path $script:Backend '.')
if ($unformatted) { throw "gofmt needed on:`n$unformatted" }
Invoke-Native 'go' @('vet', './...') $script:Backend
if (Get-Command golangci-lint -ErrorAction SilentlyContinue) { Invoke-Native 'golangci-lint' @('run') $script:Backend }
else { Write-Warning 'golangci-lint not installed: NOT RUN.' }
if (Test-Path (Join-Path $script:Frontend 'package.json')) {
    Invoke-Native 'npx' @('tsc', '--noEmit') $script:Frontend
    Invoke-Native 'npx' @('eslint', '.') $script:Frontend
    Invoke-Native 'npx' @('prettier', '--check', 'src') $script:Frontend
}
