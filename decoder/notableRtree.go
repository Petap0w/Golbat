package decoder

import (
	"sync"

	"github.com/tidwall/rtree"
)

var notableTreeMutex sync.RWMutex
var notableTree rtree.RTreeG[uint64]

func initNotableRtree() {
	// rtree.RTreeG is zero-value initialized; nothing to allocate
}

// isLookupNotable returns true for pokemon that belong in the notable secondary
// cache: hundos (15/15/15), nundos (0/0/0), XXS (size=1), or XXL (size=5).
// Size values match the existing v1 API convention (1=TINY/XXS, 5=HUGE/XXL).
// IV values of -1 indicate unknown (not yet encountered) and are never notable.
func isLookupNotable(l *PokemonLookup) bool {
	isHundo := l.Atk == 15 && l.Def == 15 && l.Sta == 15
	isNundo := l.Atk == 0 && l.Def == 0 && l.Sta == 0 // -1 means unknown, so 0/0/0 is a genuine nundo
	isSize := l.Size == 1 || l.Size == 5
	return isHundo || isNundo || isSize
}

func addPokemonToNotableTree(pokemon *Pokemon) {
	notableTreeMutex.Lock()
	notableTree.Insert(
		[2]float64{pokemon.Lon, pokemon.Lat},
		[2]float64{pokemon.Lon, pokemon.Lat},
		uint64(pokemon.Id),
	)
	notableTreeMutex.Unlock()
}

func removePokemonFromNotableTree(pokemonId uint64, lat, lon float64) {
	notableTreeMutex.Lock()
	notableTree.Delete(
		[2]float64{lon, lat},
		[2]float64{lon, lat},
		pokemonId,
	)
	notableTreeMutex.Unlock()
}
