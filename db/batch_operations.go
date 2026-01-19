package db

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// BatchUpsertPokestops performs batch insert/update for pokestops
func BatchUpsertPokestops(ctx context.Context, db *sqlx.DB, pokestops interface{}) error {
	query := `INSERT INTO pokestop 
		(id, lat, lon, name, url, enabled, lure_expire_timestamp, last_modified_timestamp, 
		 updated, quest_type, quest_timestamp, quest_target, quest_conditions, quest_rewards, 
		 quest_template, quest_title, quest_expiry, cell_id, deleted, lure_id, first_seen_timestamp, 
		 sponsor_id, partner_id, ar_scan_eligible, power_up_level, power_up_points, 
		 power_up_end_timestamp, alternative_quest_type, alternative_quest_timestamp, 
		 alternative_quest_target, alternative_quest_conditions, alternative_quest_rewards, 
		 alternative_quest_template, alternative_quest_title, alternative_quest_expiry, 
		 description, showcase_pokemon_id, showcase_pokemon_form_id, showcase_pokemon_type_id, 
		 showcase_ranking_standard, showcase_expiry, showcase_rankings, showcase_focus)
	VALUES 
		(:id, :lat, :lon, :name, :url, :enabled, :lure_expire_timestamp, :last_modified_timestamp,
		 :updated, :quest_type, :quest_timestamp, :quest_target, :quest_conditions, :quest_rewards,
		 :quest_template, :quest_title, :quest_expiry, :cell_id, :deleted, :lure_id, :first_seen_timestamp,
		 :sponsor_id, :partner_id, :ar_scan_eligible, :power_up_level, :power_up_points,
		 :power_up_end_timestamp, :alternative_quest_type, :alternative_quest_timestamp,
		 :alternative_quest_target, :alternative_quest_conditions, :alternative_quest_rewards,
		 :alternative_quest_template, :alternative_quest_title, :alternative_quest_expiry,
		 :description, :showcase_pokemon_id, :showcase_pokemon_form_id, :showcase_pokemon_type_id,
		 :showcase_ranking_standard, :showcase_expiry, :showcase_rankings, :showcase_focus)
	ON DUPLICATE KEY UPDATE
		lat = VALUES(lat),
		lon = VALUES(lon),
		name = VALUES(name),
		url = VALUES(url),
		enabled = VALUES(enabled),
		lure_expire_timestamp = VALUES(lure_expire_timestamp),
		last_modified_timestamp = VALUES(last_modified_timestamp),
		updated = VALUES(updated),
		quest_type = VALUES(quest_type),
		quest_timestamp = VALUES(quest_timestamp),
		quest_target = VALUES(quest_target),
		quest_conditions = VALUES(quest_conditions),
		quest_rewards = VALUES(quest_rewards),
		quest_template = VALUES(quest_template),
		quest_title = VALUES(quest_title),
		quest_expiry = VALUES(quest_expiry),
		cell_id = VALUES(cell_id),
		deleted = VALUES(deleted),
		lure_id = VALUES(lure_id),
		sponsor_id = VALUES(sponsor_id),
		partner_id = VALUES(partner_id),
		ar_scan_eligible = VALUES(ar_scan_eligible),
		power_up_level = VALUES(power_up_level),
		power_up_points = VALUES(power_up_points),
		power_up_end_timestamp = VALUES(power_up_end_timestamp),
		alternative_quest_type = VALUES(alternative_quest_type),
		alternative_quest_timestamp = VALUES(alternative_quest_timestamp),
		alternative_quest_target = VALUES(alternative_quest_target),
		alternative_quest_conditions = VALUES(alternative_quest_conditions),
		alternative_quest_rewards = VALUES(alternative_quest_rewards),
		alternative_quest_template = VALUES(alternative_quest_template),
		alternative_quest_title = VALUES(alternative_quest_title),
		alternative_quest_expiry = VALUES(alternative_quest_expiry),
		description = VALUES(description),
		showcase_pokemon_id = VALUES(showcase_pokemon_id),
		showcase_pokemon_form_id = VALUES(showcase_pokemon_form_id),
		showcase_pokemon_type_id = VALUES(showcase_pokemon_type_id),
		showcase_ranking_standard = VALUES(showcase_ranking_standard),
		showcase_expiry = VALUES(showcase_expiry),
		showcase_rankings = VALUES(showcase_rankings),
		showcase_focus = VALUES(showcase_focus)`

	_, err := db.NamedExecContext(ctx, query, pokestops)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_pokestops", err)
	}
	return err
}

// BatchUpsertGyms performs batch insert/update for gyms
func BatchUpsertGyms(ctx context.Context, db *sqlx.DB, gyms interface{}) error {
	query := `INSERT INTO gym 
		(id, lat, lon, name, url, last_modified_timestamp, raid_end_timestamp, raid_spawn_timestamp,
		 raid_battle_timestamp, updated, raid_pokemon_id, guarding_pokemon_id, guarding_pokemon_display,
		 available_slots, team_id, raid_level, enabled, ex_raid_eligible, in_battle,
		 raid_pokemon_move_1, raid_pokemon_move_2, raid_pokemon_form, raid_pokemon_alignment,
		 raid_pokemon_cp, raid_is_exclusive, cell_id, deleted, total_cp, first_seen_timestamp,
		 raid_pokemon_gender, sponsor_id, partner_id, raid_pokemon_costume, raid_pokemon_evolution,
		 ar_scan_eligible, power_up_level, power_up_points, power_up_end_timestamp, description,
		 defenders, rsvps)
	VALUES
		(:id, :lat, :lon, :name, :url, :last_modified_timestamp, :raid_end_timestamp, :raid_spawn_timestamp,
		 :raid_battle_timestamp, :updated, :raid_pokemon_id, :guarding_pokemon_id, :guarding_pokemon_display,
		 :available_slots, :team_id, :raid_level, :enabled, :ex_raid_eligible, :in_battle,
		 :raid_pokemon_move_1, :raid_pokemon_move_2, :raid_pokemon_form, :raid_pokemon_alignment,
		 :raid_pokemon_cp, :raid_is_exclusive, :cell_id, :deleted, :total_cp, UNIX_TIMESTAMP(),
		 :raid_pokemon_gender, :sponsor_id, :partner_id, :raid_pokemon_costume, :raid_pokemon_evolution,
		 :ar_scan_eligible, :power_up_level, :power_up_points, :power_up_end_timestamp, :description,
		 :defenders, :rsvps)
	ON DUPLICATE KEY UPDATE
		lat = VALUES(lat),
		lon = VALUES(lon),
		name = VALUES(name),
		url = VALUES(url),
		last_modified_timestamp = VALUES(last_modified_timestamp),
		raid_end_timestamp = VALUES(raid_end_timestamp),
		raid_spawn_timestamp = VALUES(raid_spawn_timestamp),
		raid_battle_timestamp = VALUES(raid_battle_timestamp),
		updated = VALUES(updated),
		raid_pokemon_id = VALUES(raid_pokemon_id),
		guarding_pokemon_id = VALUES(guarding_pokemon_id),
		guarding_pokemon_display = VALUES(guarding_pokemon_display),
		available_slots = VALUES(available_slots),
		team_id = VALUES(team_id),
		raid_level = VALUES(raid_level),
		enabled = VALUES(enabled),
		ex_raid_eligible = VALUES(ex_raid_eligible),
		in_battle = VALUES(in_battle),
		raid_pokemon_move_1 = VALUES(raid_pokemon_move_1),
		raid_pokemon_move_2 = VALUES(raid_pokemon_move_2),
		raid_pokemon_form = VALUES(raid_pokemon_form),
		raid_pokemon_alignment = VALUES(raid_pokemon_alignment),
		raid_pokemon_cp = VALUES(raid_pokemon_cp),
		raid_is_exclusive = VALUES(raid_is_exclusive),
		cell_id = VALUES(cell_id),
		deleted = VALUES(deleted),
		total_cp = VALUES(total_cp),
		raid_pokemon_gender = VALUES(raid_pokemon_gender),
		sponsor_id = VALUES(sponsor_id),
		partner_id = VALUES(partner_id),
		raid_pokemon_costume = VALUES(raid_pokemon_costume),
		raid_pokemon_evolution = VALUES(raid_pokemon_evolution),
		ar_scan_eligible = VALUES(ar_scan_eligible),
		power_up_level = VALUES(power_up_level),
		power_up_points = VALUES(power_up_points),
		power_up_end_timestamp = VALUES(power_up_end_timestamp),
		description = VALUES(description),
		defenders = VALUES(defenders),
		rsvps = VALUES(rsvps)`

	_, err := db.NamedExecContext(ctx, query, gyms)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_gyms", err)
	}
	return err
}

// BatchUpsertSpawnpoints performs batch insert/update for spawnpoints
func BatchUpsertSpawnpoints(ctx context.Context, db *sqlx.DB, spawnpoints interface{}) error {
	query := `INSERT INTO spawnpoint 
		(id, lat, lon, updated, last_seen, despawn_sec)
	VALUES 
		(:id, :lat, :lon, :updated, :last_seen, :despawn_sec)
	ON DUPLICATE KEY UPDATE
		lat = VALUES(lat),
		lon = VALUES(lon),
		updated = VALUES(updated),
		last_seen = VALUES(last_seen),
		despawn_sec = VALUES(despawn_sec)`

	_, err := db.NamedExecContext(ctx, query, spawnpoints)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_spawnpoints", err)
	}
	return err
}

// BatchUpsertIncidents performs batch insert/update for incidents
func BatchUpsertIncidents(ctx context.Context, db *sqlx.DB, incidents interface{}) error {
	query := `INSERT INTO incident 
		(id, pokestop_id, start, expiration, display_type, style, ` + "`character`" + `, 
		 updated, confirmed, slot_1_pokemon_id, slot_1_form, slot_2_pokemon_id, 
		 slot_2_form, slot_3_pokemon_id, slot_3_form)
	VALUES 
		(:id, :pokestop_id, :start, :expiration, :display_type, :style, :character,
		 :updated, :confirmed, :slot_1_pokemon_id, :slot_1_form, :slot_2_pokemon_id,
		 :slot_2_form, :slot_3_pokemon_id, :slot_3_form)
	ON DUPLICATE KEY UPDATE
		start = VALUES(start),
		expiration = VALUES(expiration),
		display_type = VALUES(display_type),
		style = VALUES(style),
		` + "`character`" + ` = VALUES(` + "`character`" + `),
		updated = VALUES(updated),
		confirmed = VALUES(confirmed),
		slot_1_pokemon_id = VALUES(slot_1_pokemon_id),
		slot_1_form = VALUES(slot_1_form),
		slot_2_pokemon_id = VALUES(slot_2_pokemon_id),
		slot_2_form = VALUES(slot_2_form),
		slot_3_pokemon_id = VALUES(slot_3_pokemon_id),
		slot_3_form = VALUES(slot_3_form)`

	_, err := db.NamedExecContext(ctx, query, incidents)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_incidents", err)
	}
	return err
}

// BatchUpsertTappables performs batch insert/update for tappables
func BatchUpsertTappables(ctx context.Context, db *sqlx.DB, tappables interface{}) error {
	query := `INSERT INTO tappable 
		(id, lat, lon, fort_id, spawn_id, type, pokemon_id, item_id, count, 
		 expire_timestamp, expire_timestamp_verified, updated)
	VALUES 
		(:id_str, :lat, :lon, :fort_id, :spawn_id, :type, :pokemon_id, :item_id, :count,
		 :expire_timestamp, :expire_timestamp_verified, :updated)
	ON DUPLICATE KEY UPDATE
		lat = VALUES(lat),
		lon = VALUES(lon),
		fort_id = VALUES(fort_id),
		spawn_id = VALUES(spawn_id),
		type = VALUES(type),
		pokemon_id = VALUES(pokemon_id),
		item_id = VALUES(item_id),
		count = VALUES(count),
		expire_timestamp = VALUES(expire_timestamp),
		expire_timestamp_verified = VALUES(expire_timestamp_verified),
		updated = VALUES(updated)`

	_, err := db.NamedExecContext(ctx, query, tappables)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_tappables", err)
	}
	return err
}

// BatchUpsertWeather performs batch insert/update for weather
func BatchUpsertWeather(ctx context.Context, db *sqlx.DB, weather interface{}) error {
	query := `INSERT INTO weather 
		(id, latitude, longitude, level, gameplay_condition, wind_direction, cloud_level, rain_level,
		 wind_level, snow_level, fog_level, special_effect_level, severity, warn_weather, updated)
	VALUES
		(:id, :latitude, :longitude, :level, :gameplay_condition, :wind_direction, :cloud_level, :rain_level,
		 :wind_level, :snow_level, :fog_level, :special_effect_level, :severity, :warn_weather, :updated/1000)
	ON DUPLICATE KEY UPDATE
		latitude = VALUES(latitude),
		longitude = VALUES(longitude),
		level = VALUES(level),
		gameplay_condition = VALUES(gameplay_condition),
		wind_direction = VALUES(wind_direction),
		cloud_level = VALUES(cloud_level),
		rain_level = VALUES(rain_level),
		wind_level = VALUES(wind_level),
		snow_level = VALUES(snow_level),
		fog_level = VALUES(fog_level),
		special_effect_level = VALUES(special_effect_level),
		severity = VALUES(severity),
		warn_weather = VALUES(warn_weather),
		updated = VALUES(updated)`

	_, err := db.NamedExecContext(ctx, query, weather)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_weather", err)
	}
	return err
}

// BatchUpsertStations performs batch insert/update for stations
func BatchUpsertStations(ctx context.Context, db *sqlx.DB, stations interface{}) error {
	query := `INSERT INTO station 
		(id, lat, lon, name, cell_id, start_time, end_time, cooldown_complete, is_battle_available, 
		 is_inactive, updated, battle_level, battle_start, battle_end, battle_pokemon_id, 
		 battle_pokemon_form, battle_pokemon_costume, battle_pokemon_gender, battle_pokemon_alignment, 
		 battle_pokemon_bread_mode, battle_pokemon_move_1, battle_pokemon_move_2, battle_pokemon_stamina, 
		 battle_pokemon_cp_multiplier, total_stationed_pokemon, total_stationed_gmax, stationed_pokemon)
	VALUES
		(:id, :lat, :lon, :name, :cell_id, :start_time, :end_time, :cooldown_complete, :is_battle_available,
		 :is_inactive, :updated, :battle_level, :battle_start, :battle_end, :battle_pokemon_id,
		 :battle_pokemon_form, :battle_pokemon_costume, :battle_pokemon_gender, :battle_pokemon_alignment,
		 :battle_pokemon_bread_mode, :battle_pokemon_move_1, :battle_pokemon_move_2, :battle_pokemon_stamina,
		 :battle_pokemon_cp_multiplier, :total_stationed_pokemon, :total_stationed_gmax, :stationed_pokemon)
	ON DUPLICATE KEY UPDATE
		lat = VALUES(lat),
		lon = VALUES(lon),
		name = VALUES(name),
		cell_id = VALUES(cell_id),
		start_time = VALUES(start_time),
		end_time = VALUES(end_time),
		cooldown_complete = VALUES(cooldown_complete),
		is_battle_available = VALUES(is_battle_available),
		is_inactive = VALUES(is_inactive),
		updated = VALUES(updated),
		battle_level = VALUES(battle_level),
		battle_start = VALUES(battle_start),
		battle_end = VALUES(battle_end),
		battle_pokemon_id = VALUES(battle_pokemon_id),
		battle_pokemon_form = VALUES(battle_pokemon_form),
		battle_pokemon_costume = VALUES(battle_pokemon_costume),
		battle_pokemon_gender = VALUES(battle_pokemon_gender),
		battle_pokemon_alignment = VALUES(battle_pokemon_alignment),
		battle_pokemon_bread_mode = VALUES(battle_pokemon_bread_mode),
		battle_pokemon_move_1 = VALUES(battle_pokemon_move_1),
		battle_pokemon_move_2 = VALUES(battle_pokemon_move_2),
		battle_pokemon_stamina = VALUES(battle_pokemon_stamina),
		battle_pokemon_cp_multiplier = VALUES(battle_pokemon_cp_multiplier),
		total_stationed_pokemon = VALUES(total_stationed_pokemon),
		total_stationed_gmax = VALUES(total_stationed_gmax),
		stationed_pokemon = VALUES(stationed_pokemon)`

	_, err := db.NamedExecContext(ctx, query, stations)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_stations", err)
	}
	return err
}

// BatchUpsertRoutes performs batch insert/update for routes
func BatchUpsertRoutes(ctx context.Context, db *sqlx.DB, routes interface{}) error {
	query := `INSERT INTO route 
		(id, name, description, distance_meters, duration_seconds, start_lat, start_lon, 
		 start_image, end_lat, end_lon, end_image, updated, reversible, tags, route_submission_start_timestamp_ms,
		 route_submission_end_timestamp_ms, type, version, path, waypoints, image, image_border_color_hex)
	VALUES
		(:id, :name, :description, :distance_meters, :duration_seconds, :start_lat, :start_lon,
		 :start_image, :end_lat, :end_lon, :end_image, :updated, :reversible, :tags, :route_submission_start_timestamp_ms,
		 :route_submission_end_timestamp_ms, :type, :version, :path, :waypoints, :image, :image_border_color_hex)
	ON DUPLICATE KEY UPDATE
		name = VALUES(name),
		description = VALUES(description),
		distance_meters = VALUES(distance_meters),
		duration_seconds = VALUES(duration_seconds),
		start_lat = VALUES(start_lat),
		start_lon = VALUES(start_lon),
		start_image = VALUES(start_image),
		end_lat = VALUES(end_lat),
		end_lon = VALUES(end_lon),
		end_image = VALUES(end_image),
		updated = VALUES(updated),
		reversible = VALUES(reversible),
		tags = VALUES(tags),
		route_submission_start_timestamp_ms = VALUES(route_submission_start_timestamp_ms),
		route_submission_end_timestamp_ms = VALUES(route_submission_end_timestamp_ms),
		type = VALUES(type),
		version = VALUES(version),
		path = VALUES(path),
		waypoints = VALUES(waypoints),
		image = VALUES(image),
		image_border_color_hex = VALUES(image_border_color_hex)`

	_, err := db.NamedExecContext(ctx, query, routes)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_routes", err)
	}
	return err
}

// BatchUpsertS2Cells performs batch insert/update for s2cells
func BatchUpsertS2Cells(ctx context.Context, db *sqlx.DB, cells interface{}) error {
	query := `INSERT INTO s2cell 
		(id, level, center_lat, center_lon, updated)
	VALUES
		(:id, :level, :center_lat, :center_lon, UNIX_TIMESTAMP())
	ON DUPLICATE KEY UPDATE
		level = VALUES(level),
		center_lat = VALUES(center_lat),
		center_lon = VALUES(center_lon),
		updated = VALUES(updated)`

	_, err := db.NamedExecContext(ctx, query, cells)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_s2cells", err)
	}
	return err
}

// BatchUpsertPlayers performs batch insert/update for players
func BatchUpsertPlayers(ctx context.Context, db *sqlx.DB, players interface{}) error {
	query := `INSERT INTO trainer 
		(name, level, team_id, battles_won, km_walked, pokemon_caught, experience, 
		 pokestops_visited, total_xp, combat_rank, combat_rating, updated)
	VALUES
		(:name, :level, :team_id, :battles_won, :km_walked, :pokemon_caught, :experience,
		 :pokestops_visited, :total_xp, :combat_rank, :combat_rating, UNIX_TIMESTAMP())
	ON DUPLICATE KEY UPDATE
		level = VALUES(level),
		team_id = VALUES(team_id),
		battles_won = VALUES(battles_won),
		km_walked = VALUES(km_walked),
		pokemon_caught = VALUES(pokemon_caught),
		experience = VALUES(experience),
		pokestops_visited = VALUES(pokestops_visited),
		total_xp = VALUES(total_xp),
		combat_rank = VALUES(combat_rank),
		combat_rating = VALUES(combat_rating),
		updated = VALUES(updated)`

	_, err := db.NamedExecContext(ctx, query, players)
	if statsCollector != nil {
		statsCollector.IncDbQuery("batch_upsert_players", err)
	}
	return err
}
