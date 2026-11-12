# Milestone 11 only. Runs the retrieval evaluation over the indexed docs and writes docs/ai-evaluation.md.
# Needs the docs indexed first (scripts\index-docs.ps1). Calls the embedding model once per question.
. "$PSScriptRoot\common.ps1"
foreach ($v in 'EM_AI_PROVIDER', 'EM_AI_API_KEY') {
    if (-not [Environment]::GetEnvironmentVariable($v)) { throw "$v is not set. See the AI section of .env.example." }
}
Invoke-Native 'go' @('run', './cmd/evaldocs') $script:Backend
