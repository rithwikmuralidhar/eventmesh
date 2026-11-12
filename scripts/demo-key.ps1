# Creates a fresh API key for the demo account and prints it once.
. "$PSScriptRoot\common.ps1"
Invoke-Native 'go' @('run', './cmd/seed', '-demo-key') $script:Backend
