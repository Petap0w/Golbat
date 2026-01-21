package decoder

import (
	"context"
	"time"

	"github.com/jellydator/ttlcache/v3"
	log "github.com/sirupsen/logrus"
	null "gopkg.in/guregu/null.v4"

	"golbat/db"
	"golbat/pogo"
	"golbat/webhooks"
)

// Incident struct.
// REMINDER! Keep hasChangesIncident updated after making changes
type Incident struct {
	Id             string   `db:"id"`
	PokestopId     string   `db:"pokestop_id"`
	StartTime      int64    `db:"start"`
	ExpirationTime int64    `db:"expiration"`
	DisplayType    int16    `db:"display_type"`
	Style          int16    `db:"style"`
	Character      int16    `db:"character"`
	Updated        int64    `db:"updated"`
	Confirmed      bool     `db:"confirmed"`
	Slot1PokemonId null.Int `db:"slot_1_pokemon_id"`
	Slot1Form      null.Int `db:"slot_1_form"`
	Slot2PokemonId null.Int `db:"slot_2_pokemon_id"`
	Slot2Form      null.Int `db:"slot_2_form"`
	Slot3PokemonId null.Int `db:"slot_3_pokemon_id"`
	Slot3Form      null.Int `db:"slot_3_form"`
}

type webhookLineup struct {
	Slot      uint8    `json:"slot"`
	PokemonId null.Int `json:"pokemon_id"`
	Form      null.Int `json:"form"`
}

//->   `id` varchar(35) NOT NULL,
//->   `pokestop_id` varchar(35) NOT NULL,
//->   `start` int unsigned NOT NULL,
//->   `expiration` int unsigned NOT NULL,
//->   `display_type` smallint unsigned NOT NULL,
//->   `style` smallint unsigned NOT NULL,
//->   `character` smallint unsigned NOT NULL,
//->   `updated` int unsigned NOT NULL,

func getIncidentRecord(ctx context.Context, db db.DbDetails, incidentId string) (*Incident, error) {
	// L1 cache check (always fast, never blocks)
	inMemoryIncident := incidentCache.Get(incidentId)
	if inMemoryIncident != nil {
		incident := inMemoryIncident.Value()
		return &incident, nil
	}

	// If Redis DISABLED, fall back to DB lookup (original behavior)
	if !IsRedisEnabled() {
		incident := Incident{}
		err := db.GeneralDb.GetContext(ctx, &incident, "SELECT * FROM incident WHERE id = ?", incidentId)
		if err != nil {
			return nil, nil // Not found or error
		}

		// Populate L1 cache
		incidentCache.Set(incidentId, incident, ttlcache.DefaultTTL)
		return &incident, nil
	}

	// Redis ENABLED: L1 only (no blocking lookups)
	return nil, nil
}

// hasChangesIncident compares two Incident structs
func hasChangesIncident(old *Incident, new *Incident) bool {
	return old.Id != new.Id ||
		old.PokestopId != new.PokestopId ||
		old.StartTime != new.StartTime ||
		old.ExpirationTime != new.ExpirationTime ||
		old.DisplayType != new.DisplayType ||
		old.Style != new.Style ||
		old.Character != new.Character ||
		old.Confirmed != new.Confirmed ||
		old.Updated != new.Updated ||
		old.Slot1PokemonId != new.Slot1PokemonId ||
		old.Slot1Form != new.Slot1Form ||
		old.Slot2PokemonId != new.Slot2PokemonId ||
		old.Slot2Form != new.Slot2Form ||
		old.Slot3PokemonId != new.Slot3PokemonId ||
		old.Slot3Form != new.Slot3Form

}

func saveIncidentRecord(ctx context.Context, db db.DbDetails, incident *Incident) {
	oldIncident, _ := getIncidentRecord(ctx, db, incident.Id)

	if oldIncident != nil && !hasChangesIncident(oldIncident, incident) {
		return
	}

	incident.Updated = time.Now().Unix()

	// Update L1 cache immediately for read consistency
	incidentCache.Set(incident.Id, *incident, ttlcache.DefaultTTL)

	// Queue write to database
	if redisEnabled {
		if err := queueWrite(ctx, "incident", "upsert", incident); err != nil {
			log.Warnf("Failed to queue incident write for %s: %s", incident.Id, err)
			// Fall back to direct DB write
			saveIncidentRecordDirect(ctx, db, incident)
		}
	} else {
		// Direct DB write if Redis not enabled
		saveIncidentRecordDirect(ctx, db, incident)
	}

	createIncidentWebhooks(ctx, db, oldIncident, incident)

	stop, _ := GetPokestopRecord(ctx, db, incident.PokestopId)
	if stop == nil {
		stop = &Pokestop{}
	}

	areas := MatchStatsGeofence(stop.Lat, stop.Lon)
	updateIncidentStats(oldIncident, incident, areas)
}

// saveIncidentRecordDirect writes directly to DB (fallback or no-Redis mode)
func saveIncidentRecordDirect(ctx context.Context, db db.DbDetails, incident *Incident) {
	res, err := db.GeneralDb.NamedExecContext(ctx,
		`INSERT INTO incident (id, pokestop_id, start, expiration, display_type, style, `+"`character`"+`, updated, confirmed, 
			slot_1_pokemon_id, slot_1_form, slot_2_pokemon_id, slot_2_form, slot_3_pokemon_id, slot_3_form) 
		VALUES (:id, :pokestop_id, :start, :expiration, :display_type, :style, :character, :updated, :confirmed, 
			:slot_1_pokemon_id, :slot_1_form, :slot_2_pokemon_id, :slot_2_form, :slot_3_pokemon_id, :slot_3_form)
		ON DUPLICATE KEY UPDATE
			start = VALUES(start),
			expiration = VALUES(expiration),
			display_type = VALUES(display_type),
			style = VALUES(style),
			`+"`character`"+` = VALUES(`+"`character`"+`),
			updated = VALUES(updated),
			confirmed = VALUES(confirmed),
			slot_1_pokemon_id = VALUES(slot_1_pokemon_id),
			slot_1_form = VALUES(slot_1_form),
			slot_2_pokemon_id = VALUES(slot_2_pokemon_id),
			slot_2_form = VALUES(slot_2_form),
			slot_3_pokemon_id = VALUES(slot_3_pokemon_id),
			slot_3_form = VALUES(slot_3_form)`,
		incident)

	statsCollector.IncDbQuery("upsert incident", err)
	if err != nil {
		log.Errorf("upsert incident %s: %s", incident.Id, err)
	}
	_ = res
}

func createIncidentWebhooks(ctx context.Context, db db.DbDetails, oldIncident *Incident, incident *Incident) {
	if oldIncident == nil || (oldIncident.ExpirationTime != incident.ExpirationTime || oldIncident.Character != incident.Character || oldIncident.Confirmed != incident.Confirmed || oldIncident.Slot1PokemonId != incident.Slot1PokemonId) {
		stop, _ := GetPokestopRecord(ctx, db, incident.PokestopId)
		if stop == nil {
			stop = &Pokestop{}
		}

		incidentHook := map[string]interface{}{
			"id":          incident.Id,
			"pokestop_id": incident.PokestopId,
			"latitude":    stop.Lat,
			"longitude":   stop.Lon,
			"pokestop_name": func() string {
				if stop.Name.Valid {
					return stop.Name.String
				} else {
					return "Unknown"
				}
			}(),
			"url":                       stop.Url.ValueOrZero(),
			"enabled":                   stop.Enabled.ValueOrZero(),
			"start":                     incident.StartTime,
			"incident_expire_timestamp": incident.ExpirationTime, // deprecated, remove old key in the future
			"expiration":                incident.ExpirationTime,
			"display_type":              incident.DisplayType,
			"style":                     incident.Style,
			"grunt_type":                incident.Character, // deprecated, remove old key in the future
			"character":                 incident.Character,
			"updated":                   incident.Updated,
			"confirmed":                 incident.Confirmed,
			"lineup":                    nil,
		}

		if incident.Slot1PokemonId.Valid {
			incidentHook["lineup"] = []webhookLineup{
				{
					Slot:      1,
					PokemonId: incident.Slot1PokemonId,
					Form:      incident.Slot1Form,
				},
				{
					Slot:      2,
					PokemonId: incident.Slot2PokemonId,
					Form:      incident.Slot2Form,
				},
				{
					Slot:      3,
					PokemonId: incident.Slot3PokemonId,
					Form:      incident.Slot3Form,
				},
			}
		}
		areas := MatchStatsGeofence(stop.Lat, stop.Lon)
		webhooksSender.AddMessage(webhooks.Invasion, incidentHook, areas)
		statsCollector.UpdateIncidentCount(areas)
	}
}

func (incident *Incident) updateFromPokestopIncidentDisplay(pokestopDisplay *pogo.PokestopIncidentDisplayProto) {
	incident.Id = pokestopDisplay.IncidentId
	incident.StartTime = int64(pokestopDisplay.IncidentStartMs / 1000)
	incident.ExpirationTime = int64(pokestopDisplay.IncidentExpirationMs / 1000)
	incident.DisplayType = int16(pokestopDisplay.IncidentDisplayType)
	if (incident.Character == int16(pogo.EnumWrapper_CHARACTER_DECOY_GRUNT_MALE) || incident.Character == int16(pogo.EnumWrapper_CHARACTER_DECOY_GRUNT_FEMALE)) && incident.Confirmed {
		log.Debugf("Incident has already been confirmed as a decoy: %s", incident.Id)
		return
	}
	characterDisplay := pokestopDisplay.GetCharacterDisplay()
	if characterDisplay != nil {
		// team := pokestopDisplay.Open
		incident.Style = int16(characterDisplay.Style)
		incident.Character = int16(characterDisplay.Character)
	} else {
		incident.Style, incident.Character = 0, 0
	}
}

func (incident *Incident) updateFromOpenInvasionCombatSessionOut(protoRes *pogo.OpenInvasionCombatSessionOutProto) {
	incident.Slot1PokemonId = null.NewInt(int64(protoRes.Combat.Opponent.ActivePokemon.PokedexId.Number()), true)
	incident.Slot1Form = null.NewInt(int64(protoRes.Combat.Opponent.ActivePokemon.PokemonDisplay.Form.Number()), true)
	for i, pokemon := range protoRes.Combat.Opponent.ReservePokemon {
		if i == 0 {
			incident.Slot2PokemonId = null.NewInt(int64(pokemon.PokedexId.Number()), true)
			incident.Slot2Form = null.NewInt(int64(pokemon.PokemonDisplay.Form.Number()), true)
		} else if i == 1 {
			incident.Slot3PokemonId = null.NewInt(int64(pokemon.PokedexId.Number()), true)
			incident.Slot3Form = null.NewInt(int64(pokemon.PokemonDisplay.Form.Number()), true)
		}
	}
	incident.Confirmed = true
}

func (incident *Incident) updateFromStartIncidentOut(proto *pogo.StartIncidentOutProto) {
	incident.Character = int16(proto.GetIncident().GetStep()[0].GetPokestopDialogue().GetDialogueLine()[0].GetCharacter())
	if incident.Character == int16(pogo.EnumWrapper_CHARACTER_GIOVANNI) ||
		incident.Character == int16(pogo.EnumWrapper_CHARACTER_DECOY_GRUNT_MALE) ||
		incident.Character == int16(pogo.EnumWrapper_CHARACTER_DECOY_GRUNT_FEMALE) {
		incident.Confirmed = true
	}
	incident.StartTime = int64(proto.Incident.GetCompletionDisplay().GetIncidentStartMs() / 1000)
	incident.ExpirationTime = int64(proto.Incident.GetCompletionDisplay().GetIncidentExpirationMs() / 1000)
}
