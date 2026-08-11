package decoder

import (
	log "github.com/sirupsen/logrus"
)

// WipePokemonSpecies removes every cached Pokemon of the given species
// (pokemon_id) from the in-memory pokemon cache and returns the number removed.
//
// Safety / concurrency:
//   - The pokemon cache and the lock-free lookup cache are each internally
//     synchronised, so Get/Delete/Range run safely alongside any other access.
//   - We iterate the lock-free pokemonLookupCache (whose items are stored by
//     value and replaced wholesale on every update) to gather candidates
//     without touching any entity mutex. We snapshot the matching ids into a
//     slice first so we never mutate the cache while ranging it.
//   - Each entity is locked before it is removed. Locking serialises us against
//     any in-flight save/encounter for that entity and guarantees a stable
//     PokemonId while we re-verify the species. We never hold more than one
//     entity lock at a time.
//   - pokemonCache.Delete does NOT run cleanup inline: the deletion event is
//     re-dispatched to the cache's single eviction dispatcher goroutine, whose
//     handler (handlePokemonEviction) takes the entity lock itself, confirms
//     the id was not re-cached, removes the lookup entry and form counts, and
//     queues the main-tree and notable-tree deletes through the ordered tree
//     workers. Holding the entity lock across Delete is therefore safe (no
//     callback re-entry), and index cleanup lands asynchronously — typically
//     within milliseconds of this function returning.
//   - The eviction dispatcher queue is bounded and shared with TTL expiries;
//     on overflow, events are dropped and the affected lookup entries leak
//     until restart (counted by the dropped-evictions metric). One event is
//     enqueued per removed entry, so avoid wiping several very large species
//     concurrently.
//
// Note: this is a point-in-time wipe of the in-memory cache only. A save that
// was already in flight, or fresh scan data arriving afterwards, can
// legitimately re-populate the species — that is expected for a cache
// operation. Rows already persisted to the database are not touched.
func WipePokemonSpecies(pokemonId int16) int {
	// Phase 1: collect candidate encounter ids from the lock-free lookup cache.
	var candidates []uint64
	pokemonLookupCache.Range(func(encounterId uint64, value PokemonLookupCacheItem) bool {
		if value.HasLookup && value.PokemonLookup.PokemonId == pokemonId {
			candidates = append(candidates, encounterId)
		}
		return true
	})

	if len(candidates) == 0 {
		return 0
	}

	// Phase 2: lock each candidate, re-verify the species under the lock, then
	// delete. Deletion is performed outside any cache Range so it cannot
	// interfere with iteration.
	removed := 0
	for _, encounterId := range candidates {
		pokemon, ok := pokemonCache.Get(encounterId)
		if !ok {
			continue // already evicted/expired between phase 1 and phase 2
		}
		pokemon.Lock("API.WipeSpecies")
		// Re-verify under the lock: the cache entry could have been replaced by
		// a different species since we snapshotted the lookup cache.
		if pokemon.PokemonId == pokemonId {
			pokemonCache.Delete(encounterId)
			removed++
		}
		pokemon.Unlock()
	}

	log.Infof("WipePokemonSpecies: removed %d cached pokemon of species %d (%d candidates)",
		removed, pokemonId, len(candidates))
	return removed
}
