# Creates the demo account, demo endpoints and demo events (idempotent).
. "$PSScriptRoot\common.ps1"
Invoke-Native 'go' @('run', './cmd/seed') $script:Backend
