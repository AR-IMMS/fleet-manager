package httpapi

import (
	"embed"
	"net/http"

	swgui "github.com/swaggest/swgui/v5emb"
)

//go:embed fleet.openapi.yaml
var apiDocsFS embed.FS

// APIDocs serves the Fleet OpenAPI document and an embedded Swagger UI.
type APIDocs struct {
	ui http.Handler
}

// NewAPIDocs creates documentation handlers that resolve the specification
// from the same HTTP origin as the UI.
func NewAPIDocs() *APIDocs {
	return &APIDocs{
		ui: swgui.New("Fleet Management API", "/openapi.yaml", "/docs/"),
	}
}

// Spec serves the embedded OpenAPI document without a filesystem dependency.
func (d *APIDocs) Spec(w http.ResponseWriter, _ *http.Request) {
	spec, err := apiDocsFS.ReadFile("fleet.openapi.yaml")
	if err != nil {
		http.Error(w, "API specification is unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(spec)
}

// UI returns the embedded Swagger UI handler.
func (d *APIDocs) UI() http.Handler {
	return d.ui
}
