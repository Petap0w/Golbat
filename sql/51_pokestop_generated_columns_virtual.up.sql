-- Convert STORED generated columns to VIRTUAL to reduce UPDATE overhead
-- STORED columns recalculate on every UPDATE with expensive JSON operations
-- VIRTUAL columns calculate on SELECT only (acceptable trade-off for quest fields)

ALTER TABLE `pokestop`
    DROP COLUMN `quest_reward_type`,
    DROP COLUMN `quest_item_id`,
    DROP COLUMN `quest_reward_amount`,
    DROP COLUMN `quest_pokemon_id`,
    DROP COLUMN `alternative_quest_pokemon_id`,
    DROP COLUMN `alternative_quest_reward_type`,
    DROP COLUMN `alternative_quest_item_id`,
    DROP COLUMN `alternative_quest_reward_amount`;

ALTER TABLE `pokestop`
    ADD COLUMN `quest_reward_type` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].type'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `quest_item_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.item_id'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `quest_reward_amount` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.amount'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `quest_pokemon_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.pokemon_id'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `alternative_quest_pokemon_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.pokemon_id'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `alternative_quest_reward_type` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].type'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `alternative_quest_item_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.item_id'),_utf8mb4'$[0]')) VIRTUAL,
    ADD COLUMN `alternative_quest_reward_amount` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.amount'),_utf8mb4'$[0]')) VIRTUAL;

-- Note: Indexes on these columns will still work with VIRTUAL columns
-- The performance impact is minimal since quest filtering is not in the hot path

