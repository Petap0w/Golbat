package main

import (
	"context"
	"net/http"

	"golbat/decoder"

	"github.com/danielgtaylor/huma/v2"
)

type pokemonNotableScanInput struct {
	Body decoder.ApiPokemonScan2
}
type pokemonNotableScanOutput struct {
	Body []*decoder.ApiPokemonResult
}

// registerCustomRoutes registers the operations carried on the prod branch that
// are not present upstream. Kept in their own file (and registration function)
// so rebasing onto upstream never conflicts inside upstream's route functions.
func registerCustomRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "scan-pokemon-notable",
		Method:        http.MethodPost,
		Path:          "/api/pokemon/notable",
		Summary:       "Search notable pokemon in a bounding box (DNF filters)",
		Description:   "Returns pokemon from the notable index (hundos, nundos, XXS, XXL) within [min,max] matching any DNF filter clause. Same request format as the v2 scan, but only the notable subset is searched. Returns a bare array.",
		Tags:          []string{"Pokemon"},
		Security:      []map[string][]string{{securitySchemeName: {}}},
		DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *pokemonNotableScanInput) (*pokemonNotableScanOutput, error) {
		return &pokemonNotableScanOutput{Body: decoder.GetNotablePokemonInArea(in.Body)}, nil
	})
}
