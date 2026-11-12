package config

import "strings"

// AI holds the optional milestone 11 settings. The zero value means every AI
// feature is off. The API key is read here and nowhere else; it must never be
// logged or returned by an endpoint (see String).
type AI struct {
	Provider         string // "gemini" or "" (disabled)
	APIKey           string
	ChatModel        string
	EmbedModel       string
	EmbedDimensions  int
	MaxOutputTokens  int
	MaxInputChars    int
	RequestsPerMin   int
	IndexDocsOnStart bool
	DocsDir          string // repository root holding docs/ and README.md
	FeatureDocs      bool
	FeatureExplain   bool
	FeatureSearch    bool
	// MinSimilarity is the cosine floor for "found in the docs"; 0 means the built-in default.
	MinSimilarity float64
}

// Default model names, checked against Google's model pages on 2026-11-12.
const (
	DefaultAIChatModel  = "gemini-3.6-flash"
	DefaultAIEmbedModel = "gemini-embedding-001"
)

// ProviderEnabled reports whether a provider is selected (not "" / "none").
func (a AI) ProviderEnabled() bool { return a.Provider == "gemini" }

// Configured reports whether a provider is selected and a key is present.
func (a AI) Configured() bool { return a.ProviderEnabled() && a.APIKey != "" }

// String redacts the key so an accidental log of the config cannot leak it.
func (a AI) String() string {
	key := "unset"
	if a.APIKey != "" {
		key = "REDACTED"
	}
	return "AI{provider=" + a.Provider + " key=" + key + " chat=" + a.ChatModel + " embed=" + a.EmbedModel + "}"
}

func loadAI(r *reader) AI {
	provider := strings.ToLower(r.str("AI_PROVIDER", ""))
	if provider == "none" {
		provider = ""
	}
	if provider != "" && provider != "gemini" {
		r.fail("AI_PROVIDER must be empty, none or gemini, got " + provider)
		provider = ""
	}
	return AI{
		Provider:         provider,
		APIKey:           r.str("AI_API_KEY", ""),
		ChatModel:        r.str("AI_CHAT_MODEL", DefaultAIChatModel),
		EmbedModel:       r.str("AI_EMBED_MODEL", DefaultAIEmbedModel),
		EmbedDimensions:  r.integer("AI_EMBED_DIMENSIONS", 768, 128, 2000),
		MaxOutputTokens:  r.integer("AI_MAX_OUTPUT_TOKENS", 512, 16, 8192),
		MaxInputChars:    r.integer("AI_MAX_INPUT_CHARS", 8000, 200, 100000),
		RequestsPerMin:   r.integer("AI_REQUESTS_PER_MINUTE", 10, 1, 10000),
		IndexDocsOnStart: r.boolean("AI_DOCS_INDEX_ON_START", false),
		DocsDir:          r.str("AI_DOCS_ROOT", ".."),
		FeatureDocs:      r.boolean("FEATURE_AI_DOCS", false),
		FeatureExplain:   r.boolean("FEATURE_AI_EXPLAIN", false),
		FeatureSearch:    r.boolean("FEATURE_AI_SEARCH", false),
		MinSimilarity:    r.float("AI_MIN_SIMILARITY", 0, 0, 1),
	}
}
