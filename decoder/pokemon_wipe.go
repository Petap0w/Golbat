package decoder

import (
	log "github.com/sirupsen/logrus"
)

// WipePokemonSpecies removes every cached Pokemon of the given species
// (pokemon_id) from the in-memory pokemon cache and returns the number removed.
//
// Safety / concurrency:
//   - The pokemon cache shards and the lock-free lookup cache are each
//     internally synchronised, so Get/Delete/Range run safely alongside any
//     other access to the cache.
//   - We iterate the lock-free pokemonLookupCache (whose *PokemonLookup values
//     are immutable snapshots, replaced wholesale on every update) to gather
//     candidates without touching any entity mutex. We snapshot the matching
//     ids into a slice first so we never mutate the cache while ranging it.
//   - Each entity is locked before it is removed. Locking serialises us against
//     any in-flight save/encounter for that entity (the heaviest other access),
//     guarantees a stable PokemonId/Lat/Lon while we re-verify the species, and
//     lets the eviction callback read Lat/Lon without a data race.
//   - pokemonCache.Delete fires the OnEviction callback synchronously, which
//     removes the entry from the R-tree, lookup cache, form counts and notable
//     tree. There is no lock-ordering inversion: the callback acquires
//     pokemonTreeMutex, and no code holds pokemonTreeMutex before re-entering a
//     cache shard.
//
// Note: this is a point-in-time wipe of the in-memory cache only. A save that
// was already in flight, or fresh scan data arriving afterwards, can legitimately
// re-populate the species — that is expected for a cache operation. Rows already
// persisted to the database are not touched.
func WipePokemonSpecies(pokemonId int16) int {
	// Phase 1: collect candidate encounter ids from the lock-free lookup cache.
	// Reading PokemonLookup.PokemonId is race-free because updatePokemonLookup
	// always stores a freshly allocated *PokemonLookup rather than mutating one.
	var candidates []uint64
	pokemonLookupCache.Range(func(encounterId uint64, value PokemonLookupCacheItem) bool {
		if value.PokemonLookup != nil && value.PokemonLookup.PokemonId == pokemonId {
			candidates = append(candidates, encounterId)
		}
		return true
	})

	if len(candidates) == 0 {
		return 0
	}

	// Phase 2: lock each candidate, re-verify the species under the lock, then
	// delete. Deletion is performed outside any cache Range so it cannot deadlock
	// against the shard mutex held during iteration.
	removed := 0
	for _, encounterId := range candidates {
		item := pokemonCache.Get(encounterId)
		if item == nil {
			continue // already evicted/expired between phase 1 and phase 2
		}
		pokemon := item.Value()
		pokemon.Lock("API.WipeSpecies")
		// Re-verify under the lock: the cache entry could have been replaced by a
		// different species since we snapshotted the lookup cache.
		if pokemon.PokemonId == pokemonId {
			// Delete synchronously fires the eviction callback, cleaning the
			// R-tree, lookup cache, form counts and notable tree for this id.
			pokemonCache.Delete(encounterId)
			removed++
		}
		pokemon.Unlock()
	}

	log.Infof("WipePokemonSpecies: removed %d cached pokemon of species %d (%d candidates)",
		removed, pokemonId, len(candidates))
	return removed
}
