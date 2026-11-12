# Builds the frontend, starts the stack with short retry delays (API serves the built UI), runs Playwright, stops the stack.
. "$PSScriptRoot\common.ps1"
$env:EM_SERVE_FRONTEND = 'true'
$env:EM_INITIAL_RETRY_DELAY = '200ms'
$env:EM_MAX_RETRY_DELAY = '1s'
$env:EM_MAX_RETRIES = '2'
$env:EM_RECONCILER_INTERVAL = '2s'
$env:EM_RECONCILER_GRACE = '2s'
$env:EM_AUTH_RATE_LIMIT_PER_MINUTE = '1000'

Invoke-Native 'npm' @('run', 'build') $script:Frontend
& (Join-Path $PSScriptRoot 'start-stack.ps1') -NoFrontend
$exit = 1
try {
    # Wait for the API to answer.
    $ready = $false
    for ($i = 0; $i -lt 40 -and -not $ready; $i++) {
        try { $ready = (Invoke-WebRequest "http://$($env:EM_HTTP_ADDR)/readyz" -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200 } catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $ready) { throw 'The API did not become ready.' }
    $env:EM_E2E_URL = "http://$($env:EM_HTTP_ADDR)"
    $env:EM_E2E_RECEIVER = "http://$($env:EM_RECEIVER_ADDR)"
    Push-Location $script:Frontend
    try { & npx playwright test; $exit = $LASTEXITCODE } finally { Pop-Location }
} finally {
    & (Join-Path $PSScriptRoot 'stop-stack.ps1')
}
exit $exit
