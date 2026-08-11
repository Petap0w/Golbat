package main

import (
	"net/http"
	"strings"
	"testing"

	"golbat/config"

	"github.com/danielgtaylor/huma/v2/humatest"
)

// TestHumaCustomRoutesE2E exercises the prod-branch custom operations through
// the full HTTP pipeline without a database.
func TestHumaCustomRoutesE2E(t *testing.T) {
	prev := config.Config.ApiSecret
	config.Config.ApiSecret = "topsecret"
	defer func() { config.Config.ApiSecret = prev }()

	_, api := humatest.New(t, newHumaConfig("test"))
	api.UseMiddleware(golbatSecretMiddleware(api))
	registerCustomRoutes(api)

	t.Run("notable without secret is 401", func(t *testing.T) {
		resp := api.Post("/api/pokemon/notable", strings.NewReader(emptyScanBody))
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", resp.Code)
		}
	})

	t.Run("notable with secret returns 202 bare array", func(t *testing.T) {
		resp := api.Post("/api/pokemon/notable", "X-Golbat-Secret: topsecret", strings.NewReader(emptyScanBody))
		if resp.Code != http.StatusAccepted {
			t.Fatalf("got %d, want 202; body=%s", resp.Code, resp.Body.String())
		}
		body := strings.TrimSpace(resp.Body.String())
		if body != "[]" {
			t.Errorf("notable body = %q, want \"[]\"", body)
		}
	})

	t.Run("notable rejects an unknown body field with 422", func(t *testing.T) {
		body := `{"min":{"lat":0,"lon":0},"max":{"lat":1,"lon":1},"filters":[],"bogus":true}`
		resp := api.Post("/api/pokemon/notable", "X-Golbat-Secret: topsecret", strings.NewReader(body))
		if resp.Code != http.StatusUnprocessableEntity {
			t.Errorf("got %d, want 422; body=%s", resp.Code, resp.Body.String())
		}
	})
}
