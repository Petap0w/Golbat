package main

import (
	"context"
	"math"
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

type pokemonWipeSpeciesInput struct {
	// Declared int64 and range-checked in the handler: Huma converts path
	// params to the field type without an overflow check, so an int16 field
	// would silently wrap out-of-range ids instead of rejecting them.
	PokedexId int64 `path:"pokedex_id" doc:"Pokedex id of the species to wipe (0-32767)"`
}
type pokemonWipeSpeciesOutput struct {
	Body struct {
		PokedexId int64 `json:"pokedex_id" doc:"Pokedex id that was wiped"`
		Removed   int   `json:"removed" doc:"Number of cached pokemon removed"`
	}
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

	huma.Register(api, huma.Operation{
		OperationID:   "wipe-pokemon-species",
		Method:        http.MethodPost,
		Path:          "/api/pokemon/species/{pokedex_id}/wipe",
		Summary:       "Wipe a pokemon species from the cache",
		Description:   "Removes every cached pokemon of the given species from the in-memory cache and its spatial indexes, returning the number removed. Point-in-time cache operation: fresh scan data can legitimately re-populate the species; database rows are not touched.",
		Tags:          []string{"Pokemon"},
		Security:      []map[string][]string{{securitySchemeName: {}}},
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, in *pokemonWipeSpeciesInput) (*pokemonWipeSpeciesOutput, error) {
		if in.PokedexId < 0 || in.PokedexId > math.MaxInt16 {
			return nil, huma.Error422UnprocessableEntity("pokedex_id out of range (0-32767)")
		}
		out := &pokemonWipeSpeciesOutput{}
		out.Body.PokedexId = in.PokedexId
		out.Body.Removed = decoder.WipePokemonSpecies(int16(in.PokedexId))
		return out, nil
	})
}
