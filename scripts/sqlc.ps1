# Regenerates the sqlc query code.
. "$PSScriptRoot\common.ps1"
Assert-Command 'sqlc' 'Run scripts\setup.ps1 (installs it with go install).'
Invoke-Native 'sqlc' @('generate') $script:Backend
