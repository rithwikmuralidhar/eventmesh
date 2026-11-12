# Runs the load-test CLI; extra arguments are passed through.
. "$PSScriptRoot\common.ps1"
Invoke-Native 'go' (@('run', './cmd/loadtest') + $args) $script:Backend
