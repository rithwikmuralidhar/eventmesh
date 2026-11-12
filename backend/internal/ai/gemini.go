package ai

import (
	"context"
	"errors"
	"fmt"
	"math"

	"google.golang.org/genai"
)

// Gemini implements Provider with the official Go SDK. It is the only file
// that imports the SDK.
type Gemini struct {
	client     *genai.Client
	chatModel  string
	embedModel string
	dims       int32
}

// NewGemini builds the client. The key is passed in by the caller (read from
// internal/config) and is never logged.
func NewGemini(ctx context.Context, apiKey, chatModel, embedModel string, dims int) (*Gemini, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Gemini{client: client, chatModel: chatModel, embedModel: embedModel, dims: int32(dims)}, nil
}

// Generate implements Provider.
func (g *Gemini) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	cfg := &genai.GenerateContentConfig{MaxOutputTokens: int32(req.MaxOutputTokens)}
	if req.System != "" {
		cfg.SystemInstruction = genai.NewContentFromText(req.System, genai.RoleUser)
	}
	if req.JSONSchema != nil {
		cfg.ResponseMIMEType = "application/json"
		cfg.ResponseJsonSchema = req.JSONSchema
	}
	resp, err := g.client.Models.GenerateContent(ctx, g.chatModel,
		[]*genai.Content{genai.NewContentFromText(req.Prompt, genai.RoleUser)}, cfg)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("gemini generate: %w", err)
	}
	out := GenerateResponse{Text: resp.Text(), Model: g.chatModel}
	if u := resp.UsageMetadata; u != nil {
		out.InputTokens, out.OutputTokens = int(u.PromptTokenCount), int(u.CandidatesTokenCount)
	}
	if out.Text == "" {
		return out, errors.New("gemini returned no text (blocked or empty)")
	}
	return out, nil
}

// Embed implements Provider. gemini-embedding-001 takes a task type; the
// output is cut to the configured dimension to fit a pgvector index.
func (g *Gemini) Embed(ctx context.Context, texts []string, task EmbedTask) ([][]float32, error) {
	taskType := "RETRIEVAL_DOCUMENT"
	if task == EmbedQuery {
		taskType = "RETRIEVAL_QUERY"
	}
	contents := make([]*genai.Content, 0, len(texts))
	for _, t := range texts {
		contents = append(contents, genai.NewContentFromText(t, genai.RoleUser))
	}
	resp, err := g.client.Models.EmbedContent(ctx, g.embedModel, contents,
		&genai.EmbedContentConfig{TaskType: taskType, OutputDimensionality: &g.dims})
	if err != nil {
		return nil, fmt.Errorf("gemini embed: %w", err)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini embed: got %d embeddings for %d texts", len(resp.Embeddings), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, e := range resp.Embeddings {
		out[i] = normalize(e.Values)
	}
	return out, nil
}

// normalize scales a truncated embedding to unit length (required for cosine
// quality after dimensionality reduction).
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}
