# Diagnostic for docs Q&A: prints retrieved chunks with scores, then the model's answer.
# Usage: scripts\ask-docs.ps1 "How are failed deliveries retried?"
. "$PSScriptRoot\common.ps1"
if ($args.Count -lt 1) { throw 'Usage: scripts\ask-docs.ps1 "your question"' }
Invoke-Native 'go' (@('run', './cmd/askdocs') + $args) $script:Backend
