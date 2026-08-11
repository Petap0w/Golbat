package decoder

// Fork addition (prod branch): notable-only pokemon scan. See
// notableRtree.go for the index itself. The DNF filter build and matcher
// deliberately duplicate internalGetPokemonInArea2 rather than refactoring
// it, so rebasing onto upstream never conflicts inside upstream's v2 scan.

import (
	"time"

	"golbat/config"

	log "github.com/sirupsen/logrus"
)

// GetNotablePokemonInArea searches only the notable secondary index
// (hundos, nundos, XXS, XXL) using the same ApiPokemonScan2 request format
// as /api/pokemon/v2/scan. This is orders of magnitude faster than a global
// v2 scan because the notable tree is a tiny subset of the full pokemon
// population.
func GetNotablePokemonInArea(retrieveParameters ApiPokemonScan2) []*ApiPokemonResult {
	dnfFilters := make(map[dnfFilterLookup][]ApiPokemonDnfFilter)

	for _, filter := range retrieveParameters.DnfFilters {
		if len(filter.Pokemon) > 0 {
			for _, keyString := range filter.Pokemon {
				pokemonId := keyString.Pokemon
				if pokemonId == 0 {
					pokemonId = -1
				}
				var formId int16 = -1
				if keyString.Form != nil {
					formId = *keyString.Form
				}
				key := dnfFilterLookup{
					pokemon: pokemonId,
					form:    formId,
				}
				dnfFilters[key] = append(dnfFilters[key], filter)
			}
		} else {
			key := dnfFilterLookup{pokemon: -1, form: -1}
			dnfFilters[key] = append(dnfFilters[key], filter)
		}
	}

	isPokemonDnfMatch := func(pokemonLookup *PokemonLookup, pvpLookup *PokemonPvpLookup, filter *ApiPokemonDnfFilter) bool {
		if filter.Iv != nil && (int16(pokemonLookup.Iv) < filter.Iv.Min || int16(pokemonLookup.Iv) > filter.Iv.Max) ||
			filter.StaIv != nil && (int16(pokemonLookup.Sta) < filter.StaIv.Min || int16(pokemonLookup.Sta) > filter.StaIv.Max) ||
			filter.AtkIv != nil && (int16(pokemonLookup.Atk) < filter.AtkIv.Min || int16(pokemonLookup.Atk) > filter.AtkIv.Max) ||
			filter.DefIv != nil && (int16(pokemonLookup.Def) < filter.DefIv.Min || int16(pokemonLookup.Def) > filter.DefIv.Max) ||
			filter.Level != nil && (int16(pokemonLookup.Level) < filter.Level.Min || int16(pokemonLookup.Level) > filter.Level.Max) ||
			filter.Cp != nil && (pokemonLookup.Cp < filter.Cp.Min || pokemonLookup.Cp > filter.Cp.Max) ||
			filter.Gender != nil && (int16(pokemonLookup.Gender) < filter.Gender.Min || int16(pokemonLookup.Gender) > filter.Gender.Max) ||
			filter.Size != nil && (int16(pokemonLookup.Size) < filter.Size.Min || int16(pokemonLookup.Size) > filter.Size.Max) {
			return false
		}

		if filter.Little != nil && (pvpLookup == nil || pvpLookup.Little < filter.Little.Min || pvpLookup.Little > filter.Little.Max) ||
			filter.Great != nil && (pvpLookup == nil || pvpLookup.Great < filter.Great.Min || pvpLookup.Great > filter.Great.Max) ||
			filter.Ultra != nil && (pvpLookup == nil || pvpLookup.Ultra < filter.Ultra.Min || pvpLookup.Ultra > filter.Ultra.Max) {
			return false
		}
		return true
	}

	notableMax := config.Config.Tuning.MaxNotablePokemonResults
	if notableMax == 0 {
		notableMax = config.Config.Tuning.MaxPokemonResults
	}

	returnKeys, _, _, _ := internalGetPokemonInAreaFromTree(
		getNotableTreeSnapshot,
		"GetNotablePokemonInArea",
		notableMax,
		retrieveParameters,
		dnfFilters,
		isPokemonDnfMatch,
	)

	results := make([]*ApiPokemonResult, 0, len(returnKeys))

	start := time.Now()
	startUnix := start.Unix()

	for _, key := range returnKeys {
		pokemon, unlock, _ := peekPokemonRecordReadOnly(key, "API.Notable")
		if pokemon != nil {
			if pokemon.ExpireTimestamp.ValueOrZero() > startUnix {
				apiPokemon := buildApiPokemonResult(pokemon)
				results = append(results, &apiPokemon)
			}
			unlock()
		}
	}

	log.Infof("GetNotablePokemonInArea - result buffer time %s, %d added", time.Since(start), len(results))

	return results
}
