# Builds a scratch database (eventmesh_plans) with 1.2 million synthetic deliveries and prints the
# query plans quoted in docs/database.md, with and without the relevant indexes. Takes about 2 minutes.
# Needs psql on PATH (or pass -Psql) and a role that may CREATE DATABASE. The development database is not touched.
param([string]$Psql = 'psql', [switch]$Keep)
. "$PSScriptRoot\common.ps1"
$admin = $env:EM_TEST_DATABASE_URL
if (-not $admin) { throw 'EM_TEST_DATABASE_URL is not set (see .env.example).' }
$target = $admin -replace '/[^/?]+(\?|$)', '/eventmesh_plans$1'
& $Psql $admin -q -c 'DROP DATABASE IF EXISTS eventmesh_plans' -c 'CREATE DATABASE eventmesh_plans'
if ($LASTEXITCODE -ne 0) { throw 'could not create the scratch database' }
$env:EM_DATABASE_URL = $target
Invoke-Native 'go' @('run', './cmd/migrate') $script:Backend
Invoke-Native $Psql @($target, '-q', '-v', 'ON_ERROR_STOP=1', '-f', 'bench/seed.sql') $script:Backend
# Twice: the first pass warms the cache, the second is the one to read.
& $Psql $target -q -f (Join-Path $script:Backend 'bench\plans.sql') | Out-Null
Invoke-Native $Psql @($target, '-q', '-f', 'bench/plans.sql') $script:Backend
if (-not $Keep) { & $Psql $admin -q -c 'DROP DATABASE IF EXISTS eventmesh_plans' }
