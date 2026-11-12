# Stops every process recorded by start-stack.ps1, including child processes (Vite spawns node).
. "$PSScriptRoot\common.ps1"
$run = Join-Path $script:Root '.run'
foreach ($f in Get-ChildItem $run -Filter *.pid -ErrorAction SilentlyContinue) {
    $id = [int](Get-Content $f.FullName)
    if (Get-Process -Id $id -ErrorAction SilentlyContinue) {
        # taskkill /T ends the whole tree. A forced stop skips graceful drain; leases recover anything in flight.
        & taskkill.exe /PID $id /T /F | Out-Null
        Write-Host "stopped $($f.BaseName) (pid $id)"
    }
    Remove-Item $f.FullName
}
