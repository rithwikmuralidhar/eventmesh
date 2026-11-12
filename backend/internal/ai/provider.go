// Package ai holds the optional milestone 11 features: documentation Q&A
// (RAG), dead-letter explanations and natural-language event search.
//
// No delivery, queue, retry, auth or rate-limit code imports this package;
// only the API wires it in. Model output is untrusted data: it is never
// executed, never used as SQL or a URL, and never triggers a write.
package ai

import "context"

// EmbedTask says what an embedding will be used for.
type EmbedTask string

// Embedding tasks.
const (
	EmbedDocument EmbedTask = "document"
	EmbedQuery    EmbedTask = "query"
)

// GenerateRequest is one chat-model call.
type GenerateRequest struct {
	System          string
	Prompt          string
	MaxOutputTokens int
	// JSONSchema, when set, asks for a JSON-only reply matching this schema
	// (a JSON-Schema object as a map). The caller still validates strictly.
	JSONSchema map[string]any
}

// GenerateResponse is the model's reply plus usage if the SDK reported it.
type GenerateResponse struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Provider is the only thing the rest of the code depends on. The Gemini
// implementation lives in gemini.go; tests use Fake.
type Provider interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Embed(ctx context.Context, texts []string, task EmbedTask) ([][]float32, error)
}
