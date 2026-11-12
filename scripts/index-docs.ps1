# Milestone 11 only. Embeds README.md and docs/ into the doc_chunks table (pgvector).
# Idempotent: unchanged chunks are skipped. Sends the docs text to the AI provider.
. "$PSScriptRoot\common.ps1"
foreach ($v in 'EM_AI_PROVIDER', 'EM_AI_API_KEY') {
    if (-not [Environment]::GetEnvironmentVariable($v)) { throw "$v is not set. See the AI section of .env.example." }
}
Invoke-Native 'go' @('run', './cmd/indexdocs') $script:Backend
