package httpapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEmbeddedOpenAPIMatchesDocsAndParses(t *testing.T) {
	embedded, err := swaggerFS.ReadFile("swagger/openapi.yaml")
	require.NoError(t, err)
	canonical, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(canonical), string(embedded),
		"copy docs/openapi.yaml to backend/internal/httpapi/swagger/openapi.yaml")

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(canonical, &doc))
	require.NotEmpty(t, doc["paths"])
}
