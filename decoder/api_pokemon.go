package decoder

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"golbat/config"
	"golbat/geo"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/rtree"
	"github.com/jellydator/ttlcache/v3"
)

const earthRadiusKm = 6371

type ApiPokemonAvailableResult struct {
	PokemonId int16 `json:"id"`
	Form      int16 `json:"form"`
	Count     int   `json:"count"`
}

func GetAvailablePokemon() []*ApiPokemonAvailableResult {
	type pokemonFormKey struct {
		pokemonId int16
		form      int16
	}

	start := time.Now()

	pkmnMap := make(map[pokemonFormKey]int)
	pokemonLookupCache.Range(func(key uint64, pokemon PokemonLookupCacheItem) bool {
		pkmnMap[pokemonFormKey{pokemon.PokemonLookup.PokemonId, pokemon.PokemonLookup.Form}]++
		return true
	})

	var available []*ApiPokemonAvailableResult
	for key, count := range pkmnMap {

		pkmn := &ApiPokemonAvailableResult{
			PokemonId: key.pokemonId,
			Form:      key.form,
			Count:     count,
		}
		available = append(available, pkmn)
	}

	log.Infof("GetAvailablePokemon - total time %s (locked time --)", time.Since(start))

	return available
}

// Pokemon search

type ApiPokemonSearch struct {
	Min       geo.Location `json:"min"`
	Max       geo.Location `json:"max"`
	Center    geo.Location `json:"center"`
	Limit     int          `json:"limit"`
	SearchIds []int16      `json:"searchIds"`
}

func calculateHypotenuse(a, b float64) float64 {
	return math.Sqrt(a*a + b*b)
}

func toRadians(deg float64) float64 {
	return deg * math.Pi / 180
}

func haversine(start, end geo.Location) float64 {
	lat1Rad := toRadians(start.Latitude)
	lat2Rad := toRadians(end.Latitude)
	deltaLat := toRadians(end.Latitude - start.Latitude)
	deltaLon := toRadians(end.Longitude - start.Longitude)

	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusKm * c
}

func SearchPokemon(request ApiPokemonSearch) ([]*Pokemon, error) {
	start := time.Now()
	results := make([]*Pokemon, 0, request.Limit)
	pokemonMatched := 0

	if request.SearchIds == nil {
		return nil, fmt.Errorf("SearchPokemon - no search ids provided")
	}
	if haversine(request.Min, request.Max) > config.Config.Tuning.MaxPokemonDistance {
		return nil, fmt.Errorf("SearchPokemon - the distance between max and min points is greater than the configurable max distance")
	}

	pokemonTreeMutex.RLock()
	pokemonTree2 := pokemonTree.Copy()
	pokemonTreeMutex.RUnlock()

	maxPokemon := config.Config.Tuning.MaxPokemonResults
	if request.Limit > 0 && request.Limit < maxPokemon {
		maxPokemon = request.Limit
	}
	pokemonSkipped := 0
	pokemonScanned := 0
	maxDistance := calculateHypotenuse(request.Max.Longitude-request.Min.Longitude, request.Max.Latitude-request.Min.Latitude) / 2
	if maxDistance == 0 {
		maxDistance = 10
	}
	pokemonTree2.Nearby(
		rtree.BoxDist[float64, uint64]([2]float64{request.Center.Longitude, request.Center.Latitude}, [2]float64{request.Center.Longitude, request.Center.Latitude}, nil),
		func(min, max [2]float64, pokemonId uint64, dist float64) bool {
			pokemonLookupItem, inCache := pokemonLookupCache.Load(pokemonId)
			if !inCache {
				pokemonSkipped++
				// Did not find cached result, something amiss?
				return true
			}

			pokemonScanned++
			if dist > maxDistance {
				log.Infof("SearchPokemon - result would exceed maximum distance (%f), stopping scan", maxDistance)
				return false
			}

			found := slices.Contains(request.SearchIds, pokemonLookupItem.PokemonLookup.PokemonId)

			if found {
				if pokemonCacheEntry := pokemonCache.Get(strconv.FormatUint(pokemonId, 10)); pokemonCacheEntry != nil {
					pokemon := pokemonCacheEntry.Value()
					results = append(results, &pokemon)
					pokemonMatched++

					if pokemonMatched > maxPokemon {
						log.Infof("SearchPokemon - result would exceed maximum size (%d), stopping scan", maxPokemon)
						return false
					}
				}
			}

			return true
		},
	)

	log.Infof("SearchPokemon - scanned %d pokemon, total time %s, %d returned", pokemonScanned, time.Since(start), len(results))
	return results, nil
}

// Get one result

func GetOnePokemon(pokemonId uint64) *Pokemon {
	if item := pokemonCache.Get(strconv.FormatUint(pokemonId, 10)); item != nil {
		pokemon := item.Value()
		return &pokemon
	}
	return nil
}

type ApiPokemonLiveStatsResult struct {
    PokemonCached      int `json:"pokemon_cached"`
    PokemonNoTimer      int `json:"pokemon_no_timer"`
    PokemonExpired      int `json:"pokemon_expired"`
	PokemonActive      int `json:"pokemon_active"`
	PokemonActiveIv    int `json:"pokemon_active_iv"`
	PokemonActive100iv int `json:"pokemon_active_100iv"`
	PokemonActiveShiny int `json:"pokemon_active_shiny"`
	PokemonOldestExpiry int64 `json:"pokemon_oldest_expiry"`
    PokemonDeleteExpiredCached      int `json:"pokemon_delete_expired_cached"`
    PokemonDeleteExpiredExpired      int `json:"pokemon_delete_expired_expired"`
    PokemonLookupCached      int `json:"pokemon_lookup_cached"`
    PokemonLookupNoTimer      int `json:"pokemon_lookup_no_timer"`
    PokemonLookupExpired      int `json:"pokemon_lookup_expired"`
	PokemonLookupActive      int `json:"pokemon_lookup_active"`
	PokemonLookupActiveIv    int `json:"pokemon_lookup_active_iv"`
	PokemonLookupActive100iv int `json:"pokemon_lookup_active_100iv"`
}

func GetLiveStatsPokemon() *ApiPokemonLiveStatsResult {
	start := time.Now()
	now := time.Now().Unix()

	liveStats := &ApiPokemonLiveStatsResult{
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		9999999999,
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		0,
	}

	pokemonCache.Range(func(pokemonCacheEntry *ttlcache.Item[string, Pokemon]) bool {
	    pokemon := pokemonCacheEntry.Value()
	    expiry := pokemonCacheEntry.ExpiresAt()
	    liveStats.PokemonCached++
	    if int64(valueOrMinus1(pokemon.ExpireTimestamp)) == -1 {
	        liveStats.PokemonNoTimer++
	    }
	    if int64(valueOrMinus1(pokemon.ExpireTimestamp)) < now && int64(valueOrMinus1(pokemon.ExpireTimestamp)) > -1 {
            if int64(valueOrMinus1(pokemon.ExpireTimestamp)) < liveStats.PokemonOldestExpiry {
                liveStats.PokemonOldestExpiry = int64(valueOrMinus1(pokemon.ExpireTimestamp))
                tm := time.Unix(liveStats.PokemonOldestExpiry, 0)
                log.Infof("apiLiveStats - Debug PokemonCache Oldest ExpiredTimestamp : %d pokemon_oldest_expiry, %s time ago, ttl expires at %d (in %s)", liveStats.PokemonOldestExpiry, time.Since(tm), expiry.Unix(), time.Until(expiry))
            }
	        liveStats.PokemonExpired++
	    }
		if int64(valueOrMinus1(pokemon.ExpireTimestamp)) > now {
			liveStats.PokemonActive++
			if !pokemon.Iv.IsZero() {
				liveStats.PokemonActiveIv++
			}
			if bool(pokemon.Shiny.ValueOrZero()) {
				liveStats.PokemonActiveShiny++
			}
			if int(pokemon.Iv.ValueOrZero()) == 100 {
				liveStats.PokemonActive100iv++
			}
		}
		return true
	})

	pokemonLookupCache.Range(func(key uint64, pokemon PokemonLookupCacheItem) bool {
		liveStats.PokemonLookupCached++
		if pokemon.PokemonLookup.ExpireTimestamp == -1 {
		    liveStats.PokemonLookupNoTimer++
        }
		if pokemon.PokemonLookup.ExpireTimestamp < now && pokemon.PokemonLookup.ExpireTimestamp > -1 {
		    liveStats.PokemonLookupExpired++
        }
		if pokemon.PokemonLookup.ExpireTimestamp > now {
 			liveStats.PokemonLookupActive++
 			if pokemon.PokemonLookup.Iv > -1 {
 				liveStats.PokemonLookupActiveIv++
 			}
 			if pokemon.PokemonLookup.Iv == 100 {
 				liveStats.PokemonLookupActive100iv++
 			}
 		}
 		return true
 	})

/*
 	pokemonCache.DeleteExpired()

	pokemonCache.Range(func(pokemonCacheEntry *ttlcache.Item[string, Pokemon]) bool {
	    pokemon := pokemonCacheEntry.Value()
	    liveStats.PokemonDeleteExpiredCached++
	    if int64(valueOrMinus1(pokemon.ExpireTimestamp)) < now && int64(valueOrMinus1(pokemon.ExpireTimestamp)) > -1 {
	        liveStats.PokemonDeleteExpiredExpired++
	    }
 		return true
 	})
*/
    tm := time.Unix(liveStats.PokemonOldestExpiry, 0)

	log.Infof("apiLiveStats - PokemonCache : %d pokemon_cached, %d pokemon_no_timer, %d pokemon_expired, %d pokemon_active_oldest_expiry, %d pokemon_active, %d pokemon_active_iv, %d pokemon_active_100iv, %d pokemon_active_shiny, total time %s", liveStats.PokemonCached, liveStats.PokemonNoTimer, liveStats.PokemonExpired, liveStats.PokemonOldestExpiry, liveStats.PokemonActive, liveStats.PokemonActiveIv, liveStats.PokemonActive100iv, liveStats.PokemonActiveShiny, time.Since(start))
	log.Infof("apiLiveStats - PokemonLookupCache : %d pokemon_lookup_cached, %d pokemon_lookup_no_timer, %d pokemon_lookup_expired, %d pokemon_lookup_active, %d pokemon_lookup_active_iv, %d pokemon_lookup_active_100iv, total time %s", liveStats.PokemonLookupCached, liveStats.PokemonLookupNoTimer, liveStats.PokemonLookupExpired, liveStats.PokemonLookupActive, liveStats.PokemonLookupActiveIv, liveStats.PokemonLookupActive100iv, time.Since(start))
	log.Infof("apiLiveStats - PokemonCache Oldest ExpiredTimestamp : %d pokemon_oldest_expiry, %s time ago", liveStats.PokemonOldestExpiry, time.Since(tm))
//	log.Infof("apiLiveStats - PokemonCache after DeleteExpired : %d pokemon_delete_expired_cached, %d pokemon_delete_expired_expired", liveStats.PokemonDeleteExpiredCached, liveStats.PokemonDeleteExpiredExpired)
	return liveStats
}
