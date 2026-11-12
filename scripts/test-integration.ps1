# Integration tests against real PostgreSQL and Redis. Each run makes its own database and Redis key prefix.
. "$PSScriptRoot\common.ps1"
foreach ($v in 'EM_TEST_DATABASE_URL', 'EM_TEST_REDIS_URL') {
    if (-not [Environment]::GetEnvironmentVariable($v)) { throw "$v is not set. Set it in .env (see .env.example)." }
}
$race = @()
if (Get-Command gcc -ErrorAction SilentlyContinue) { $env:CGO_ENABLED = '1'; $race = @('-race') }
else { Write-Warning '-race NOT RUN: no C compiler (gcc) on PATH.' }
Invoke-Native 'go' (@('test', '-count=1') + $race + @('./tests/...')) $script:Backend
