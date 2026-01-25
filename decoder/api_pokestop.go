package decoder

import (
	"encoding/json"
	"time"

	"golbat/geo"

	log "github.com/sirupsen/logrus"
)

type ApiPokestopScan2 struct {
	Min        geo.Location           `json:"min"`
	Max        geo.Location           `json:"max"`
	Limit      int                    `json:"limit"`
	DnfFilters []ApiPokestopDnfFilter `json:"filters"`
}

type ApiPokestopDnfFilter struct {
	// Showcase filters
	ShowcasePokemonId          *int    `json:"showcase_pokemon_id"`           // Filter by showcase pokemon ID
	ShowcasePokemonForm        *int    `json:"showcase_pokemon_form_id"`      // Filter by showcase pokemon form ID
	ShowcasePokemonType        *int    `json:"showcase_pokemon_type_id"`      // Filter by showcase pokemon type ID
	HasShowcase                *bool   `json:"has_showcase"`                  // Filter by whether pokestop has an active showcase
	ShowcaseFocusType          *string `json:"showcase_focus_type"`           // Filter by showcase focus type ("buddy", "pokemon", "type")
	ShowcaseExpiryMax          *int64  `json:"showcase_expiry_max"`           // Filter for showcases expiring in less than X seconds from now
	ShowcaseRankingsMaxEntries *int    `json:"showcase_rankings_max_entries"` // Filter for showcases with maximum X total_entries
}

type PokestopScan2Result struct {
	Pokestops        []*Pokestop `json:"pokestops"`
	ProcessingTime   string      `json:"processing_time"`    // Total processing time in human-readable format (e.g., "123.456ms")
	ProcessingTimeMs int64       `json:"processing_time_ms"` // Total processing time in milliseconds
	Scanned          int         `json:"scanned"`            // Number of pokestops scanned
	Matched          int         `json:"matched"`            // Number of pokestops that matched filters
	Skipped          int         `json:"skipped"`            // Number of pokestops skipped (gyms, not in cache, etc.)
}

// isPokestopDnfMatch checks if a pokestop matches a single DNF filter (AND logic within filter)
func isPokestopDnfMatch(pokestop *Pokestop, filter *ApiPokestopDnfFilter) bool {
	// Showcase filters
	if filter.HasShowcase != nil {
		// A pokestop has a showcase if showcase_expiry is valid and in the future
		hasShowcase := pokestop.ShowcaseExpiry.Valid && pokestop.ShowcaseExpiry.Int64 > time.Now().Unix()
		if *filter.HasShowcase != hasShowcase {
			return false
		}
	}

	// If HasShowcase is false and we're checking showcase fields, skip showcase checks
	if filter.HasShowcase != nil && !*filter.HasShowcase {
		// If we're explicitly filtering for no showcase, don't check other showcase fields
		return true
	}

	// Only check other showcase fields if the pokestop actually has a showcase
	hasActiveShowcase := pokestop.ShowcaseExpiry.Valid && pokestop.ShowcaseExpiry.Int64 > time.Now().Unix()
	if !hasActiveShowcase {
		// If no active showcase, only pass if we're explicitly filtering for no showcase
		if filter.HasShowcase != nil && !*filter.HasShowcase {
			return true
		}
		// Otherwise, fail if any showcase-specific filters are set
		if filter.ShowcasePokemonId != nil || filter.ShowcasePokemonForm != nil ||
			filter.ShowcasePokemonType != nil || filter.ShowcaseFocusType != nil ||
			filter.ShowcaseExpiryMax != nil || filter.ShowcaseRankingsMaxEntries != nil {
			return false
		}
		return true
	}

	// Check showcase pokemon ID (can be NULL for buddy/type showcases)
	if filter.ShowcasePokemonId != nil {
		if !pokestop.ShowcasePokemon.Valid || pokestop.ShowcasePokemon.Int64 != int64(*filter.ShowcasePokemonId) {
			return false
		}
	}

	// Check showcase pokemon form
	if filter.ShowcasePokemonForm != nil {
		if !pokestop.ShowcasePokemonForm.Valid || pokestop.ShowcasePokemonForm.Int64 != int64(*filter.ShowcasePokemonForm) {
			return false
		}
	}

	// Check showcase pokemon type
	if filter.ShowcasePokemonType != nil {
		if !pokestop.ShowcasePokemonType.Valid || pokestop.ShowcasePokemonType.Int64 != int64(*filter.ShowcasePokemonType) {
			return false
		}
	}

	// Check showcase focus type (parse JSON from showcase_focus)
	if filter.ShowcaseFocusType != nil {
		if !pokestop.ShowcaseFocus.Valid || pokestop.ShowcaseFocus.String == "" {
			return false
		}

		var focus map[string]any
		if err := json.Unmarshal([]byte(pokestop.ShowcaseFocus.String), &focus); err != nil {
			log.Debugf("isPokestopDnfMatch - failed to parse showcase_focus for %s: %v", pokestop.Id, err)
			return false
		}

		focusType, ok := focus["type"].(string)
		if !ok || focusType != *filter.ShowcaseFocusType {
			return false
		}
	}

	// Check showcase expiry (expiring in less than X seconds from now)
	if filter.ShowcaseExpiryMax != nil {
		if !pokestop.ShowcaseExpiry.Valid {
			return false
		}
		now := time.Now().Unix()
		secondsUntilExpiry := pokestop.ShowcaseExpiry.Int64 - now
		if secondsUntilExpiry < 0 || secondsUntilExpiry > *filter.ShowcaseExpiryMax {
			return false
		}
	}

	// Check showcase rankings total_entries (parse JSON from showcase_rankings)
	if filter.ShowcaseRankingsMaxEntries != nil {
		if !pokestop.ShowcaseRankings.Valid || pokestop.ShowcaseRankings.String == "" {
			return false
		}

		var rankings map[string]any
		if err := json.Unmarshal([]byte(pokestop.ShowcaseRankings.String), &rankings); err != nil {
			log.Debugf("isPokestopDnfMatch - failed to parse showcase_rankings for %s: %v", pokestop.Id, err)
			return false
		}

		totalEntries, ok := rankings["total_entries"].(float64)
		if !ok {
			// Try as int if float conversion fails
			totalEntriesInt, okInt := rankings["total_entries"].(int)
			if !okInt {
				return false
			}
			totalEntries = float64(totalEntriesInt)
		}

		if int(totalEntries) > *filter.ShowcaseRankingsMaxEntries {
			return false
		}
	}

	return true
}

// GetPokestopsInArea2 searches for pokestops using fortTree, fortLookupCache, and pokestopCache
// Uses DNF (Disjunctive Normal Form) filter system: OR logic between filter groups, AND logic within each group
// Similar to Pokemon Scan V2
func GetPokestopsInArea2(search ApiPokestopScan2) (*PokestopScan2Result, error) {
	start := time.Now()
	results := make([]*Pokestop, 0, search.Limit)
	pokestopMatched := 0
	pokestopScanned := 0
	pokestopSkipped := 0

	if len(search.DnfFilters) == 0 {
		elapsed := time.Since(start)
		return &PokestopScan2Result{
			Pokestops:        results,
			ProcessingTime:   elapsed.String(),
			ProcessingTimeMs: elapsed.Milliseconds(),
			Scanned:          pokestopScanned,
			Matched:          pokestopMatched,
			Skipped:          pokestopSkipped,
		}, nil
	}

	// Validate geographic bounds
	minLocation := search.Min
	maxLocation := search.Max
	if minLocation.Latitude == 0 && minLocation.Longitude == 0 &&
		maxLocation.Latitude == 0 && maxLocation.Longitude == 0 {
		log.Warnf("GetPokestopsInArea2 - no geographic bounds provided, returning empty results")
		elapsed := time.Since(start)
		return &PokestopScan2Result{
			Pokestops:        results,
			ProcessingTime:   elapsed.String(),
			ProcessingTimeMs: elapsed.Milliseconds(),
			Scanned:          pokestopScanned,
			Matched:          pokestopMatched,
			Skipped:          pokestopSkipped,
		}, nil
	}

	// Set limit
	maxPokestops := search.Limit
	if maxPokestops <= 0 {
		maxPokestops = 500
	}
	if maxPokestops > 10000 {
		maxPokestops = 10000
	}

	// Copy R-Tree for safe concurrent access
	fortTreeMutex.RLock()
	fortTree2 := fortTree.Copy()
	fortTreeMutex.RUnlock()

	lockedTime := time.Since(start)

	// Use R-Tree to find candidates in bounding box
	var candidateIDs []string
	fortTree2.Search(
		[2]float64{minLocation.Longitude, minLocation.Latitude},
		[2]float64{maxLocation.Longitude, maxLocation.Latitude},
		func(min, max [2]float64, fortId string) bool {
			candidateIDs = append(candidateIDs, fortId)
			return true // continue iteration
		},
	)

	log.Debugf("GetPokestopsInArea2 - R-Tree found %d candidates in bbox", len(candidateIDs))

	// Process candidates with DNF filter logic (OR between groups)
	for _, fortId := range candidateIDs {
		// Quick filter: check if it's a pokestop (not gym) using lookup cache
		fortTreeMutex.RLock()
		lookup, exists := fortLookupCache[fortId]
		fortTreeMutex.RUnlock()

		if !exists || lookup.IsGym {
			pokestopSkipped++
			continue // Skip gyms
		}

		// Get full pokestop from L1 cache
		stop := getPokestopFromCache(fortId)
		if stop == nil {
			pokestopSkipped++
			continue // Not in cache
		}

		pokestop := stop.Value()
		pokestopScanned++

		// DNF logic: try each filter group (OR logic)
		matched := false
		for i := 0; i < len(search.DnfFilters); i++ {
			if isPokestopDnfMatch(&pokestop, &search.DnfFilters[i]) {
				matched = true
				break // Found a match, no need to check other groups
			}
		}

		if matched {
			results = append(results, &pokestop)
			pokestopMatched++

			if pokestopMatched >= maxPokestops {
				log.Debugf("GetPokestopsInArea2 - reached limit of %d results", maxPokestops)
				break
			}
		}
	}

	elapsed := time.Since(start)
	log.Infof("GetPokestopsInArea2 - scan time %s (locked time %s), %d scanned, %d skipped, %d returned",
		elapsed, lockedTime, pokestopScanned, pokestopSkipped, pokestopMatched)

	return &PokestopScan2Result{
		Pokestops:        results,
		ProcessingTime:   elapsed.String(),
		ProcessingTimeMs: elapsed.Milliseconds(),
		Scanned:          pokestopScanned,
		Matched:          pokestopMatched,
		Skipped:          pokestopSkipped,
	}, nil
}
