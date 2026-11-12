# Runs the load-test experiments from docs/performance.md. Each configuration restarts the stack so the
# worker count (or other setting) takes effect, then writes loadtest-results\<label>.json.
# Usage: scripts\run-experiments.ps1 [-Events 1000]
param([int]$Events = 1000)
. "$PSScriptRoot\common.ps1"
New-Item -ItemType Directory -Force (Join-Path $script:Root 'loadtest-results') | Out-Null

# High limits so the generator is not throttled by EventMesh's own protection.
$base = @{ EM_INGEST_RATE_LIMIT = '100000'; EM_INGEST_RATE_BURST = '100000'; EM_API_RATE_LIMIT = '100000'; EM_API_RATE_BURST = '100000';
           EM_AUTH_RATE_LIMIT_PER_MINUTE = '1000'; EM_RECONCILER_GRACE = '5s' }

function Invoke-Config([string]$Label, [hashtable]$Env, [string[]]$LoadArgs) {
    $saved = @{}
    foreach ($k in $base.Keys + $Env.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k, 'Process') }
    foreach ($k in $base.Keys) { [Environment]::SetEnvironmentVariable($k, $base[$k], 'Process') }
    foreach ($k in $Env.Keys) { [Environment]::SetEnvironmentVariable($k, $Env[$k], 'Process') }
    try {
        & (Join-Path $PSScriptRoot 'start-stack.ps1') -NoFrontend | Out-Null
        for ($i = 0; $i -lt 40; $i++) { try { if ((Invoke-WebRequest "http://$($env:EM_HTTP_ADDR)/readyz" -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200) { break } } catch { Start-Sleep -Milliseconds 500 } }
        $out = Join-Path $script:Root "loadtest-results\$Label.json"
        $all = @('-api', "http://$($env:EM_HTTP_ADDR)", '-events', "$Events", '-label', $Label, '-out', $out) + $LoadArgs
        & (Join-Path $PSScriptRoot 'loadtest.ps1') @all
    } finally {
        & (Join-Path $PSScriptRoot 'stop-stack.ps1') | Out-Null
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
    }
}

# Experiment 1: vary EM_WORKER_COUNT, everything else fixed (3 healthy endpoints).
foreach ($w in 1, 5, 10, 25) { Invoke-Config "workers-$w" @{ EM_WORKER_COUNT = "$w" } @('-healthy', '3', '-clients', '20') }
# Experiment 2: vary the receiver's response delay (3 slow endpoints, 10 workers).
foreach ($ms in 0, 100, 500) { Invoke-Config "delay-${ms}ms" @{ EM_WORKER_COUNT = '10' } @('-healthy', '0', '-slow', '3', '-slow-ms', "$ms", '-clients', '20') }