package httpapi

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// swaggerFS holds the Swagger UI assets and a copy of docs/openapi.yaml.
// The embed directive cannot reach outside the module, so the spec is copied here; a unit
// test fails if the copy drifts from docs/openapi.yaml.
//
//go:embed swagger
var swaggerFS embed.FS

const swaggerPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>EventMesh API</title>
<link rel="stylesheet" href="/docs/assets/swagger-ui.css"></head>
<body><div id="ui"></div>
<script src="/docs/assets/swagger-ui-bundle.js"></script>
<script>window.ui = SwaggerUIBundle({ url: "/docs/openapi.yaml", dom_id: "#ui" });</script>
</body></html>`

func registerDocs(r *gin.Engine) {
	r.GET("/docs", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerPage)) })
	serve := func(name, contentType string) gin.HandlerFunc {
		return func(c *gin.Context) {
			data, err := swaggerFS.ReadFile("swagger/" + name)
			if err != nil {
				c.Status(http.StatusNotFound)
				return
			}
			c.Data(http.StatusOK, contentType, data)
		}
	}
	r.GET("/docs/openapi.yaml", serve("openapi.yaml", "application/yaml"))
	r.GET("/docs/assets/swagger-ui.css", serve("swagger-ui.css", "text/css"))
	r.GET("/docs/assets/swagger-ui-bundle.js", serve("swagger-ui-bundle.js", "application/javascript"))
}
