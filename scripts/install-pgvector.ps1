# Copies a built pgvector (vector.dll, vector.control, vector--*.sql in .tools\pgvector) into the PostgreSQL folder.
# Needs an ELEVATED PowerShell (Run as administrator) because PostgreSQL lives under Program Files.
# Build steps: docs/windows-setup.md section 5. Restart the PostgreSQL service afterwards.
param([string]$PgRoot = 'E:\Program Files\PostgreSQL\18')
. "$PSScriptRoot\common.ps1"
$src = Join-Path $script:Root '.tools\pgvector'
if (-not (Test-Path (Join-Path $src 'vector.dll'))) { throw "No built pgvector in $src." }
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { throw 'Run this from an elevated (administrator) PowerShell.' }
Copy-Item (Join-Path $src 'vector.dll') (Join-Path $PgRoot 'lib') -Force
Copy-Item (Join-Path $src 'vector.control') (Join-Path $PgRoot 'share\extension') -Force
Copy-Item (Join-Path $src 'vector--*.sql') (Join-Path $PgRoot 'share\extension') -Force
Write-Host 'pgvector files installed. Restart the PostgreSQL service if CREATE EXTENSION does not find them.'
