# One-time setup: dependencies, sqlc, Playwright browsers, .env.
. "$PSScriptRoot\common.ps1"
Assert-Command 'go' 'Install Go.'
Assert-Command 'node' 'Install Node.js LTS.'
if (-not (Test-Path (Join-Path $script:Root '.env'))) {
    Copy-Item (Join-Path $script:Root '.env.example') (Join-Path $script:Root '.env')
    Write-Host 'Created .env from .env.example - review the database URL.'
}
Invoke-Native 'go' @('mod', 'download') $script:Backend
Invoke-Native 'go' @('install', 'github.com/sqlc-dev/sqlc/cmd/sqlc@latest') $script:Backend
if (Test-Path (Join-Path $script:Frontend 'package.json')) {
    Invoke-Native 'npm' @('ci') $script:Frontend
    Invoke-Native 'npx' @('playwright', 'install', 'chromium') $script:Frontend
}
Write-Host 'Setup complete. Next: scripts\check-prereqs.ps1, scripts\migrate.ps1, scripts\seed.ps1'
