package decoder

import (
	"context"
	"encoding/json"
	"fmt"
	"golbat/db"
	"golbat/pogo"
	"golbat/util"
	"time"

	"github.com/jellydator/ttlcache/v3"
	log "github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v4"
)

type Route struct {
	Id               string      `db:"id"`
	Name             string      `db:"name"`
	Shortcode        string      `db:"shortcode"`
	Description      string      `db:"description"`
	DistanceMeters   int64       `db:"distance_meters"`
	DurationSeconds  int64       `db:"duration_seconds"`
	EndFortId        string      `db:"end_fort_id"`
	EndImage         string      `db:"end_image"`
	EndLat           float64     `db:"end_lat"`
	EndLon           float64     `db:"end_lon"`
	Image            string      `db:"image"`
	ImageBorderColor string      `db:"image_border_color"`
	Reversible       bool        `db:"reversible"`
	StartFortId      string      `db:"start_fort_id"`
	StartImage       string      `db:"start_image"`
	StartLat         float64     `db:"start_lat"`
	StartLon         float64     `db:"start_lon"`
	Tags             null.String `db:"tags"`
	Type             int8        `db:"type"`
	Updated          int64       `db:"updated"`
	Version          int64       `db:"version"`
	Waypoints        string      `db:"waypoints"`
}

func getRouteRecord(db db.DbDetails, id string) (*Route, error) {
	// L1 CACHE ONLY - no blocking I/O!
	inMemoryRoute := routeCache.Get(id)
	if inMemoryRoute != nil {
		route := inMemoryRoute.Value()
		return &route, nil
	}

	// Not in L1 cache = return nil
	return nil, nil
}

// hasChangesRoute compares two Route structs
func hasChangesRoute(old *Route, new *Route) bool {
	return old.Name != new.Name ||
		old.Shortcode != new.Shortcode ||
		old.Description != new.Description ||
		old.DistanceMeters != new.DistanceMeters ||
		old.DurationSeconds != new.DurationSeconds ||
		old.EndFortId != new.EndFortId ||
		!floatAlmostEqual(old.EndLat, new.EndLat, floatTolerance) ||
		!floatAlmostEqual(old.EndLon, new.EndLon, floatTolerance) ||
		old.Image != new.Image ||
		old.ImageBorderColor != new.ImageBorderColor ||
		old.Reversible != new.Reversible ||
		old.StartFortId != new.StartFortId ||
		!floatAlmostEqual(old.StartLat, new.StartLat, floatTolerance) ||
		!floatAlmostEqual(old.StartLon, new.StartLon, floatTolerance) ||
		old.Tags != new.Tags ||
		old.Type != new.Type ||
		old.Version != new.Version ||
		old.Waypoints != new.Waypoints
}

func saveRouteRecord(db db.DbDetails, route *Route) error {
	oldRoute, _ := getRouteRecord(db, route.Id)

	if oldRoute != nil && !hasChangesRoute(oldRoute, route) {
		if oldRoute.Updated > time.Now().Unix()-900 {
			return nil
		}
	}

	// Update L1 cache immediately
	routeCache.Set(route.Id, *route, ttlcache.DefaultTTL)

	// Queue write to database
	ctx := context.TODO()
	if redisEnabled {
		if err := queueWrite(ctx, "route", "upsert", route); err != nil {
			log.Warnf("Failed to queue route write for %s: %s", route.Id, err)
			return saveRouteRecordDirect(db, route)
		}
	} else {
		return saveRouteRecordDirect(db, route)
	}

	return nil
}

// saveRouteRecordDirect writes directly to DB (fallback or no-Redis mode)
func saveRouteRecordDirect(db db.DbDetails, route *Route) error {
	_, err := db.GeneralDb.NamedExec(
		`INSERT INTO route (
			id, name, shortcode, description, distance_meters,
			duration_seconds, end_fort_id, end_image,
			end_lat, end_lon, image, image_border_color, 
			reversible, start_fort_id, start_image, 
			start_lat, start_lon, tags, type, 
			updated, version, waypoints
		)
		VALUES (
			:id, :name, :shortcode, :description, :distance_meters,
			:duration_seconds, :end_fort_id,
			:end_image, :end_lat, :end_lon, :image, 
			:image_border_color, :reversible, 
			:start_fort_id, :start_image, :start_lat, 
			:start_lon, :tags, :type, :updated, 
			:version, :waypoints
		)
		ON DUPLICATE KEY UPDATE
			name = VALUES(name),
			shortcode = VALUES(shortcode),
			description = VALUES(description),
			distance_meters = VALUES(distance_meters),
			duration_seconds = VALUES(duration_seconds),
			end_fort_id = VALUES(end_fort_id),
			end_image = VALUES(end_image),
			end_lat = VALUES(end_lat),
			end_lon = VALUES(end_lon),
			image = VALUES(image),
			image_border_color = VALUES(image_border_color),
			reversible = VALUES(reversible),
			start_fort_id = VALUES(start_fort_id),
			start_image = VALUES(start_image),
			start_lat = VALUES(start_lat),
			start_lon = VALUES(start_lon),
			tags = VALUES(tags),
			type = VALUES(type),
			updated = VALUES(updated),
			version = VALUES(version),
			waypoints = VALUES(waypoints)`,
		route)

	statsCollector.IncDbQuery("upsert route", err)
	if err != nil {
		return fmt.Errorf("upsert route error: %w", err)
	}
	return nil
}

func (route *Route) updateFromSharedRouteProto(sharedRouteProto *pogo.SharedRouteProto) {
	route.Name = sharedRouteProto.GetName()
	// NOTE: Some names have more than 50 runes, which won't fit in our varchar(50).
	if truncateStr, truncated := util.TruncateUTF8(route.Name, 50); truncated {
		log.Warnf("truncating name for route id '%s'",
			route.Id,
		)
		route.Name = truncateStr
	}
	if sharedRouteProto.GetShortCode() != "" {
		route.Shortcode = sharedRouteProto.GetShortCode()
	}
	route.Description = sharedRouteProto.GetDescription()
	// NOTE: Some descriptions have more than 255 runes, which won't fit in our
	// varchar(255).
	if truncateStr, truncated := util.TruncateUTF8(route.Description, 255); truncated {
		log.Warnf("truncating description for route id '%s'. Orig description: %s",
			route.Id,
			route.Description,
		)
		route.Description = truncateStr
	}
	route.DistanceMeters = sharedRouteProto.GetRouteDistanceMeters()
	route.DurationSeconds = sharedRouteProto.GetRouteDurationSeconds()
	route.EndFortId = sharedRouteProto.GetEndPoi().GetAnchor().GetFortId()
	route.EndImage = sharedRouteProto.GetEndPoi().GetImageUrl()
	route.EndLat = sharedRouteProto.GetEndPoi().GetAnchor().GetLatDegrees()
	route.EndLon = sharedRouteProto.GetEndPoi().GetAnchor().GetLngDegrees()
	route.Image = sharedRouteProto.GetImage().GetImageUrl()
	route.ImageBorderColor = sharedRouteProto.GetImage().GetBorderColorHex()
	route.Reversible = sharedRouteProto.GetReversible()
	route.StartFortId = sharedRouteProto.GetStartPoi().GetAnchor().GetFortId()
	route.StartImage = sharedRouteProto.GetStartPoi().GetImageUrl()
	route.StartLat = sharedRouteProto.GetStartPoi().GetAnchor().GetLatDegrees()
	route.StartLon = sharedRouteProto.GetStartPoi().GetAnchor().GetLngDegrees()
	route.Type = int8(sharedRouteProto.GetType())
	route.Updated = time.Now().Unix()
	route.Version = sharedRouteProto.GetVersion()
	waypoints, _ := json.Marshal(sharedRouteProto.GetWaypoints())
	route.Waypoints = string(waypoints)

	if len(sharedRouteProto.GetTags()) > 0 {
		tags, _ := json.Marshal(sharedRouteProto.GetTags())
		route.Tags = null.StringFrom(string(tags))
	}
}

func UpdateRouteRecordWithSharedRouteProto(db db.DbDetails, sharedRouteProto *pogo.SharedRouteProto) error {
	routeMutex, _ := routeStripedMutex.GetLock(sharedRouteProto.GetId())
	routeMutex.Lock()
	defer routeMutex.Unlock()

	route, err := getRouteRecord(db, sharedRouteProto.GetId())
	if err != nil {
		return err
	}

	if route == nil {
		route = &Route{
			Id: sharedRouteProto.GetId(),
		}
	}

	route.updateFromSharedRouteProto(sharedRouteProto)
	saveError := saveRouteRecord(db, route)
	return saveError
}
