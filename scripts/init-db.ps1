# One-time: creates the eventmesh role and database. psql prompts for the postgres superuser password.
# Usage: scripts\init-db.ps1 [-PsqlPath "E:\Program Files\PostgreSQL\18\bin\psql.exe"]
param([string]$PsqlPath)
. "$PSScriptRoot\common.ps1"
if (-not $PsqlPath) {
    $cmd = Get-Command psql -ErrorAction SilentlyContinue
    if ($cmd) { $PsqlPath = $cmd.Source }
    else {
        $svc = Get-CimInstance Win32_Service | Where-Object { $_.Name -like 'postgresql*' } | Select-Object -First 1
        if ($svc -and $svc.PathName -match '"([^"]+)\\pg_ctl\.exe"') { $PsqlPath = Join-Path $Matches[1] 'psql.exe' }
    }
}
if (-not $PsqlPath -or -not (Test-Path $PsqlPath)) { throw 'psql.exe not found. Pass -PsqlPath.' }
$sql = @"
DO `$`$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'eventmesh') THEN
    CREATE ROLE eventmesh LOGIN PASSWORD 'eventmesh' CREATEDB;
  END IF;
END `$`$;
SELECT 'CREATE DATABASE eventmesh OWNER eventmesh' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'eventmesh')\gexec
"@
$sql | & $PsqlPath -h 127.0.0.1 -U postgres -v ON_ERROR_STOP=1
if ($LASTEXITCODE -ne 0) { throw 'init-db failed' }
Write-Host 'Role and database "eventmesh" are ready (development password; change it for anything shared).'