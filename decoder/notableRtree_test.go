package decoder

import (
	"testing"
	"time"

	"github.com/guregu/null/v6"
)

func TestIsLookupNotable(t *testing.T) {
	cases := []struct {
		name string
		l    PokemonLookup
		want bool
	}{
		{"hundo", PokemonLookup{Atk: 15, Def: 15, Sta: 15, Size: 3}, true},
		{"nundo", PokemonLookup{Atk: 0, Def: 0, Sta: 0, Size: 3}, true},
		{"xxs", PokemonLookup{Atk: 7, Def: 8, Sta: 9, Size: 1}, true},
		{"xxl", PokemonLookup{Atk: 7, Def: 8, Sta: 9, Size: 5}, true},
		{"ordinary", PokemonLookup{Atk: 7, Def: 8, Sta: 9, Size: 3}, false},
		{"unknown IVs are not a nundo", PokemonLookup{Atk: -1, Def: -1, Sta: -1, Size: -1}, false},
		{"near-hundo", PokemonLookup{Atk: 15, Def: 15, Sta: 14, Size: 3}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLookupNotable(&c.l); got != c.want {
				t.Errorf("isLookupNotable(%+v) = %v, want %v", c.l, got, c.want)
			}
		})
	}
}

func notableInTree(id uint64, lat, lon float64) bool {
	found := false
	notableTreeMutex.RLock()
	notableTree.Search([2]float64{lon, lat}, [2]float64{lon, lat}, func(_, _ [2]float64, v uint64) bool {
		if v == id {
			found = true
			return false
		}
		return true
	})
	notableTreeMutex.RUnlock()
	return found
}

// waitNotableInTree polls for the async notable tree worker to apply queued
// mutations; returns whether the point reached the wanted state in time.
func waitNotableInTree(id uint64, lat, lon float64, want bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if notableInTree(id, lat, lon) == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestSyncNotableTreeTransitions drives the four lifecycle transitions the
// notable index must handle: become notable, move while notable, stop being
// notable, and eviction cleanup.
func TestSyncNotableTreeTransitions(t *testing.T) {
	const id uint64 = 920001
	notable := PokemonLookup{PokemonId: 25, Atk: 15, Def: 15, Sta: 15, Size: 3}
	ordinary := PokemonLookup{PokemonId: 25, Atk: 7, Def: 8, Sta: 9, Size: 3}
	p := &Pokemon{PokemonData: PokemonData{Id: Uint64Str(id), Lat: 10.5, Lon: 20.5, PokemonId: 25, Form: null.IntFrom(0)}}
	p.oldValues.Lat, p.oldValues.Lon = 10.5, 20.5

	// Not notable -> not notable: no point appears.
	syncNotableTree(p, PokemonLookupCacheItem{PokemonLookup: ordinary, HasLookup: true}, true, ordinary)
	if waitNotableInTree(id, 10.5, 20.5, true) {
		t.Fatal("ordinary->ordinary update must not add a notable point")
	}

	// Became notable: point appears at the current position.
	syncNotableTree(p, PokemonLookupCacheItem{PokemonLookup: ordinary, HasLookup: true}, true, notable)
	if !waitNotableInTree(id, 10.5, 20.5, true) {
		t.Fatal("became-notable point never appeared")
	}

	// Stayed notable, moved: point follows to the new position.
	p.oldValues.Lat, p.oldValues.Lon = p.Lat, p.Lon
	p.Lat, p.Lon = 11.5, 21.5
	syncNotableTree(p, PokemonLookupCacheItem{PokemonLookup: notable, HasLookup: true}, true, notable)
	if !waitNotableInTree(id, 11.5, 21.5, true) {
		t.Fatal("moved point never appeared at the new position")
	}
	if !waitNotableInTree(id, 10.5, 20.5, false) {
		t.Fatal("moved point still present at the old position")
	}

	// Stopped being notable (e.g. Ditto re-identification): point removed.
	p.oldValues.Lat, p.oldValues.Lon = p.Lat, p.Lon
	syncNotableTree(p, PokemonLookupCacheItem{PokemonLookup: notable, HasLookup: true}, true, ordinary)
	if !waitNotableInTree(id, 11.5, 21.5, false) {
		t.Fatal("no-longer-notable point was not removed")
	}

	// Eviction cleanup: a notable lookup item enqueues a delete.
	syncNotableTree(p, PokemonLookupCacheItem{PokemonLookup: ordinary, HasLookup: true}, true, notable)
	if !waitNotableInTree(id, 11.5, 21.5, true) {
		t.Fatal("re-added point never appeared")
	}
	notableTreeEvictionCleanup(PokemonLookupCacheItem{PokemonLookup: notable, HasLookup: true}, id, 11.5, 21.5)
	if !waitNotableInTree(id, 11.5, 21.5, false) {
		t.Fatal("eviction cleanup did not remove the point")
	}
}
