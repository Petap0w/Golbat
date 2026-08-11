package decoder

// Fork addition (prod branch): secondary R-tree of "notable" pokemon —
// hundos, nundos, XXS, XXL — kept in sync from the same lookup-cache
// updates and evictions as the main pokemon tree. POST /api/pokemon/notable
// searches only this small subset, orders of magnitude cheaper than a
// global v2 scan.
//
// The index follows the mainline tree-writer architecture: all runtime
// mutations are ordered through a dedicated treeEvictor worker, so savers
// holding entity locks never touch notableTreeMutex, and scans read a
// shared snapshot refreshed at most once per treeSnapshotMaxAge (hits are
// re-verified against pokemonLookupCache, so snapshot/worker lag only
// affects candidate discovery).

import (
	"sync"
	"sync/atomic"

	"github.com/tidwall/rtree"
)

var notableTreeMutex sync.RWMutex
var notableTree rtree.RTreeG[uint64]
var notableTreeSnapshot atomic.Pointer[treeSnapshot[uint64]]
var notableTreeEvictor *treeEvictor[uint64]

func initNotableRtree() {
	notableTreeEvictor = newTreeEvictor[uint64]("notable", treeEvictorQueueSize, treeEvictorBatchSize, flushNotableTreeEvictions)
}

func flushNotableTreeEvictions(entries []treeEvictionEntry[uint64]) {
	flushTreeEvictions(&notableTreeMutex, &notableTree, entries)
}

func getNotableTreeSnapshot() *rtree.RTreeG[uint64] {
	return refreshTreeSnapshot(&notableTreeSnapshot, &notableTreeMutex, &notableTree)
}

// isLookupNotable returns true for pokemon that belong in the notable
// secondary index: hundos (15/15/15), nundos (0/0/0), XXS (size=1), or XXL
// (size=5). Size values match the v1 API convention (1=TINY/XXS,
// 5=HUGE/XXL). IV values of -1 mean unknown (not yet encountered) and are
// never notable.
func isLookupNotable(l *PokemonLookup) bool {
	isHundo := l.Atk == 15 && l.Def == 15 && l.Sta == 15
	isNundo := l.Atk == 0 && l.Def == 0 && l.Sta == 0 // -1 means unknown, so 0/0/0 is a genuine nundo
	isSize := l.Size == 1 || l.Size == 5
	return isHundo || isNundo || isSize
}

// syncNotableTree maintains the notable index across a lookup update. Called
// from updatePokemonLookup with the pre-update lookup item and the freshly
// stored lookup, under the entity lock (or from preload, which is
// pre-traffic). Position bookkeeping mirrors the main tree's save path: the
// tree point sits at the previous save's position, which is
// pokemon.oldValues when this save moved the pokemon — and the paths where
// oldValues is not snapshotted (on-get rehydration, preload) always arrive
// with existed=false, so they never consult it.
//
// The eviction/re-add race self-heals the same way the main tree does: an
// eviction that fired while the saver held the entity lock has already
// enqueued the notable delete, existed comes back false, and the insert
// enqueued here lands after it on the single ordered worker.
func syncNotableTree(pokemon *Pokemon, oldItem PokemonLookupCacheItem, existed bool, newLookup PokemonLookup) {
	wasNotable := existed && oldItem.HasLookup && isLookupNotable(&oldItem.PokemonLookup)
	isNowNotable := isLookupNotable(&newLookup)
	if !wasNotable && !isNowNotable {
		return
	}
	pokemonId := uint64(pokemon.Id)
	switch {
	case isNowNotable && !wasNotable:
		notableTreeEvictor.EnqueueInsert(pokemonId, pokemon.Lat, pokemon.Lon)
	case !isNowNotable: // was notable, no longer (e.g. Ditto re-identification)
		notableTreeEvictor.Enqueue(pokemonId, pokemon.oldValues.Lat, pokemon.oldValues.Lon)
	default: // stayed notable; move the point if the pokemon moved
		if pokemon.Lat != pokemon.oldValues.Lat || pokemon.Lon != pokemon.oldValues.Lon {
			notableTreeEvictor.Enqueue(pokemonId, pokemon.oldValues.Lat, pokemon.oldValues.Lon)
			notableTreeEvictor.EnqueueInsert(pokemonId, pokemon.Lat, pokemon.Lon)
		}
	}
}

// notableTreeEvictionCleanup removes an evicted pokemon's notable point.
// Called from handlePokemonEviction with the lookup item it LoadAndDeleted,
// on the cache's eviction dispatcher goroutine — TryEnqueue, never a
// blocking send (a dropped delete leaves a ghost point, which scans
// tolerate: candidates are verified against the lookup cache).
func notableTreeEvictionCleanup(item PokemonLookupCacheItem, pokemonId uint64, lat, lon float64) {
	if item.HasLookup && isLookupNotable(&item.PokemonLookup) {
		notableTreeEvictor.TryEnqueue(pokemonId, lat, lon)
	}
}
