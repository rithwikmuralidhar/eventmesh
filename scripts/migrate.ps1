# Applies pending migrations, or rolls back one with -Down.
param([switch]$Down)
. "$PSScriptRoot\common.ps1"
$goArgs = @('run', './cmd/migrate')
if ($Down) { $goArgs += '-down' }
Invoke-Native 'go' $goArgs $script:Backend
