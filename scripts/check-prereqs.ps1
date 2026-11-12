# Verifies the toolchain and services EventMesh needs and prints PASS/FAIL for each.
. "$PSScriptRoot\common.ps1"
$ErrorActionPreference = 'Continue'
$script:failed = 0
function Report([string]$Name, [bool]$Ok, [string]$Detail) {
    if ($Ok) { Write-Host ("PASS  {0,-18} {1}" -f $Name, $Detail) -ForegroundColor Green }
    else { Write-Host ("FAIL  {0,-18} {1}" -f $Name, $Detail) -ForegroundColor Red; $script:failed++ }
}

foreach ($t in 'go', 'node', 'npm', 'git') {
    $cmd = Get-Command $t -ErrorAction SilentlyContinue
    if ($cmd) {
        $verArg = if ($t -eq 'go') { 'version' } else { '--version' }
        Report $t $true ((& $cmd.Source $verArg) | Select-Object -First 1)
    } else { Report $t $false "not on PATH" }
}

Push-Location $script:Backend
try {
    $out = (& go run ./cmd/dbcheck 2>&1) | Out-String
    Report 'PostgreSQL' ($LASTEXITCODE -eq 0) (($out.Trim() -split "`n")[0])
    $out = (& go run ./cmd/redischeck 2>&1) | Out-String
    Report 'Memurai (Redis)' ($LASTEXITCODE -eq 0) (($out.Trim() -split "`n")[0])
} finally { Pop-Location }

$apiPort = if ($env:EM_HTTP_ADDR) { [int]($env:EM_HTTP_ADDR -split ':')[-1] } else { 8080 }
foreach ($port in $apiPort, 9000, 9090, 9091, 3000) {
    $c = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($c) {
        $p = Get-Process -Id $c.OwningProcess -ErrorAction SilentlyContinue
        Report "port $port" $false "in use by $($p.ProcessName) (pid $($c.OwningProcess)); run scripts\stop-stack.ps1 if it is EventMesh"
    } else { Report "port $port" $true 'free' }
}
if ($script:failed -gt 0) { Write-Host "`n$($script:failed) check(s) failed." -ForegroundColor Red; exit 1 }
Write-Host "`nAll checks passed." -ForegroundColor Green
