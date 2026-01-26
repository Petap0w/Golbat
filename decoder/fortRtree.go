package decoder

import (
	"context"
	"sync"

	"github.com/jellydator/ttlcache/v3"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/rtree"
	"gopkg.in/guregu/null.v4"

	"golbat/db"
)

type FortLookup struct {
	IsGym           bool
	Lure            int16
	RaidLevel       int8
	RaidPokemonId   int16
	QuestRewardType int16
	QuestRewardId   int16
}

var fortLookupCache map[string]FortLookup
var fortTreeMutex sync.RWMutex
var fortTree rtree.RTreeG[string]

func initFortRtree() {
	fortLookupCache = make(map[string]FortLookup)

	// Set up OnEviction callbacks for pokestop caches
	for i := range pokestopCache {
		pokestopCache[i].OnEviction(func(ctx context.Context, ev ttlcache.EvictionReason, v *ttlcache.Item[string, Pokestop]) {
			p := v.Value()
			removePokestopFromTree(&p)
		})
	}

	// Set up OnEviction callbacks for gym caches
	for i := range gymCache {
		gymCache[i].OnEviction(func(ctx context.Context, ev ttlcache.EvictionReason, v *ttlcache.Item[string, Gym]) {
			g := v.Value()
			removeGymFromTree(&g)
		})
	}
}

type IdRecord struct {
	Id string `db:"id"`
}

func LoadAllPokestops(details db.DbDetails) {
	var place IdRecord
	rows, err := details.GeneralDb.Queryx("SELECT id FROM pokestop")
	count := 0
	if err != nil {
		log.Errorf("FortRTree: Load Pokestops %s", err)
		return
	}
	for rows.Next() {
		if count%1000 == 0 {
			log.Infof("Loaded %d pokestops", count)
		}
		count++
		err := rows.StructScan(&place)
		if err != nil {
			log.Fatalln(err)
		}
		GetPokestopRecord(context.Background(), details, place.Id)
	}
	log.Infof("Loaded %d pokestops [finished]", count)
}

func LoadAllGyms(details db.DbDetails) {
	var place IdRecord
	rows, err := details.GeneralDb.Queryx("SELECT id FROM gym")
	count := 0
	if err != nil {
		log.Errorf("FortRTree: Load Gyms %s", err)
		return
	}
	for rows.Next() {
		if count%1000 == 0 {
			log.Infof("Loaded %d gyms", count)
		}
		count++
		err := rows.StructScan(&place)
		if err != nil {
			log.Fatalln(err)
		}
		GetGymRecord(context.Background(), details, place.Id)
	}
	log.Infof("Loaded %d gyms [finished]", count)
}

func fortRtreeUpdatePokestopOnGet(pokestop *Pokestop) {
	fortTreeMutex.RLock()
	_, inMap := fortLookupCache[pokestop.Id]
	fortTreeMutex.RUnlock()
	if !inMap {
		addPokestopToTree(pokestop)
		// assumes lat,lon unchanged since ejected from cache, so do not add to rtree
		updatePokestopLookup(pokestop)
	}
}

func fortRtreeUpdateGymOnGet(gym *Gym) {
	fortTreeMutex.RLock()
	_, inMap := fortLookupCache[gym.Id]
	fortTreeMutex.RUnlock()
	if !inMap {
		addGymToTree(gym)
		// assumes lat,lon unchanged since ejected from cache, so do not add to rtree
		updateGymLookup(gym)
	}
}

func updatePokestopLookup(pokestop *Pokestop) {
	fortTreeMutex.Lock()
	fortLookupCache[pokestop.Id] = FortLookup{
		IsGym: false,
		Lure:  pokestop.LureId,
		//		RaidLevel:       pokestop.RaidLevel,
		//		RaidPokemonId:   pokestop.RaidPokemonId,
		//		QuestRewardType: pokestop.QuestRewardType,
		//		QuestRewardId:   pokestop.QuestRewardId,
	}
	fortTreeMutex.Unlock()
}

func updateGymLookup(gym *Gym) {
	fortTreeMutex.Lock()
	fortLookupCache[gym.Id] = FortLookup{
		IsGym:         true,
		RaidLevel:     int8(gym.RaidLevel.ValueOrZero()),
		RaidPokemonId: int16(gym.RaidPokemonId.ValueOrZero()),
	}
	fortTreeMutex.Unlock()
}

func addPokestopToTree(pokestop *Pokestop) {
	//	log.Infof("FortRtree - add pokestop %s, lat %f lon %f", pokestop.Id, pokestop.Lat, pokestop.Lon)

	fortTreeMutex.Lock()
	fortTree.Insert([2]float64{pokestop.Lon, pokestop.Lat}, [2]float64{pokestop.Lon, pokestop.Lat}, pokestop.Id)
	fortTreeMutex.Unlock()
}

func addGymToTree(gym *Gym) {
	//	log.Infof("FortRtree - add gym %s, lat %f lon %f", gym.Id, gym.Lat, gym.Lon)

	fortTreeMutex.Lock()
	fortTree.Insert([2]float64{gym.Lon, gym.Lat}, [2]float64{gym.Lon, gym.Lat}, gym.Id)
	fortTreeMutex.Unlock()
}

func removePokestopFromTree(pokestop *Pokestop) {
	fortTreeMutex.Lock()
	beforeLen := fortTree.Len()
	fortTree.Delete([2]float64{pokestop.Lon, pokestop.Lat}, [2]float64{pokestop.Lon, pokestop.Lat}, pokestop.Id)
	afterLen := fortTree.Len()
	fortTreeMutex.Unlock()
	delete(fortLookupCache, pokestop.Id)

	if beforeLen != afterLen+1 {
		log.Debugf("FortRtree - UNEXPECTED removing pokestop %s, lat %f lon %f size %d->%d",
			pokestop.Id, pokestop.Lat, pokestop.Lon, beforeLen, afterLen)
	}
}

func removeGymFromTree(gym *Gym) {
	fortTreeMutex.Lock()
	beforeLen := fortTree.Len()
	fortTree.Delete([2]float64{gym.Lon, gym.Lat}, [2]float64{gym.Lon, gym.Lat}, gym.Id)
	afterLen := fortTree.Len()
	fortTreeMutex.Unlock()
	delete(fortLookupCache, gym.Id)

	if beforeLen != afterLen+1 {
		log.Debugf("FortRtree - UNEXPECTED removing gym %s, lat %f lon %f size %d->%d",
			gym.Id, gym.Lat, gym.Lon, beforeLen, afterLen)
	}
}

// GetFortTreeStats returns current R-Tree and lookup cache sizes for monitoring
func GetFortTreeStats() (treeSize int, lookupSize int) {
	fortTreeMutex.RLock()
	treeSize = fortTree.Len()
	lookupSize = len(fortLookupCache)
	fortTreeMutex.RUnlock()
	return
}

// ClearPokestopQuestsInGeofence clears quest data from pokestops within a geofence
// Uses R-Tree for efficient spatial querying
// Returns number of pokestops cleared
func ClearPokestopQuestsInGeofence(geofence *geojson.Feature) int {
	bbox := geofence.Geometry.Bound()
	cleared := 0

	// Query R-Tree for forts in bounding box
	fortTreeMutex.RLock()
	var candidateIDs []string
	fortTree.Search(
		[2]float64{bbox.Min.Lon(), bbox.Min.Lat()},
		[2]float64{bbox.Max.Lon(), bbox.Max.Lat()},
		func(min, max [2]float64, fortId string) bool {
			candidateIDs = append(candidateIDs, fortId)
			return true // continue iteration
		},
	)
	fortTreeMutex.RUnlock()

	log.Debugf("FortRtree - ClearQuests: R-Tree bbox query found %d candidates for geofence", len(candidateIDs))

	// Process candidates: check geofence and clear quest fields
	for _, fortId := range candidateIDs {
		// Check if it's a pokestop (not gym)
		fortTreeMutex.RLock()
		lookup, exists := fortLookupCache[fortId]
		fortTreeMutex.RUnlock()

		if !exists || lookup.IsGym {
			continue // Skip gyms
		}

		// Get pokestop from L1 cache
		stop := getPokestopFromCache(fortId)
		if stop == nil {
			continue // Not in cache
		}

		pokestop := stop.Value()

		// Precise geofence check (point-in-polygon)
		if !isPointInGeofence(pokestop.Lat, pokestop.Lon, geofence) {
			continue
		}

		// Clear quest fields
		pokestop.QuestType = null.Int{}
		pokestop.QuestTimestamp = null.Int{}
		pokestop.QuestTarget = null.Int{}
		pokestop.QuestConditions = null.String{}
		pokestop.QuestRewards = null.String{}
		pokestop.QuestTemplate = null.String{}
		pokestop.QuestTitle = null.String{}
		pokestop.QuestExpiry = null.Int{}
		pokestop.AlternativeQuestType = null.Int{}
		pokestop.AlternativeQuestTimestamp = null.Int{}
		pokestop.AlternativeQuestTarget = null.Int{}
		pokestop.AlternativeQuestConditions = null.String{}
		pokestop.AlternativeQuestRewards = null.String{}
		pokestop.AlternativeQuestTemplate = null.String{}
		pokestop.AlternativeQuestTitle = null.String{}
		pokestop.AlternativeQuestExpiry = null.Int{}

		// Update L1 cache with cleared quest (Redis will be lazy updated on next save)
		setPokestopCache(pokestop.Id, pokestop, ttlcache.DefaultTTL)
		cleared++
	}

	return cleared
}

// isPointInGeofence checks if a point (lat, lon) is within a geofence polygon
func isPointInGeofence(lat, lon float64, geofence *geojson.Feature) bool {
	point := orb.Point{lon, lat}

	// Handle different geometry types
	switch geom := geofence.Geometry.(type) {
	case orb.Polygon:
		return planar.PolygonContains(geom, point)
	case orb.MultiPolygon:
		return planar.MultiPolygonContains(geom, point)
	default:
		log.Warnf("FortRtree - Unsupported geometry type for geofence: %T", geofence.Geometry)
		return false
	}
}
