# Builds and starts receiver, worker and API in the background (logs in logs\, PIDs in .run\),
# then the Vite dev server when the frontend exists. Stop with scripts\stop-stack.ps1.
param([switch]$NoFrontend)
. "$PSScriptRoot\common.ps1"
$logs = Join-Path $script:Root 'logs'; $run = Join-Path $script:Root '.run'; $bin = Join-Path $script:Root 'bin'
New-Item -ItemType Directory -Force $logs, $run, $bin | Out-Null
if (Get-ChildItem $run -Filter *.pid -ErrorAction SilentlyContinue) { throw 'A stack is already recorded in .run\. Run scripts\stop-stack.ps1 first.' }

foreach ($name in 'api', 'worker', 'receiver') {
    Invoke-Native 'go' @('build', '-o', (Join-Path $bin "$name.exe"), "./cmd/$name") $script:Backend
}
function Start-Part([string]$Name, [string]$File, [string[]]$PartArgs, [string]$Dir) {
    $splat = @{
        FilePath = $File; WorkingDirectory = $Dir; PassThru = $true; WindowStyle = 'Hidden'
        RedirectStandardOutput = (Join-Path $logs "$Name.log"); RedirectStandardError = (Join-Path $logs "$Name.err.log")
    }
    if ($PartArgs.Count -gt 0) { $splat.ArgumentList = $PartArgs }
    $p = Start-Process @splat
    Set-Content -Path (Join-Path $run "$Name.pid") -Value $p.Id
    Write-Host "started $Name (pid $($p.Id))"
}
Start-Part 'receiver' (Join-Path $bin 'receiver.exe') @() $script:Backend
Start-Part 'worker' (Join-Path $bin 'worker.exe') @() $script:Backend
Start-Part 'api' (Join-Path $bin 'api.exe') @() $script:Backend
if (-not $NoFrontend -and (Test-Path (Join-Path $script:Frontend 'package.json'))) {
    Start-Part 'frontend' (Get-Command npm.cmd).Source @('run', 'dev') $script:Frontend
}
Write-Host 'API http://127.0.0.1:8080  receiver http://127.0.0.1:9000  UI http://127.0.0.1:3000'
