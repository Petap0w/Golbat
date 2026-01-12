-- ============================================================================
-- MANUAL ROLLBACK SCRIPT FOR MIGRATION 51
-- Converts VIRTUAL generated columns back to STORED
-- ============================================================================
-- 
-- USE THIS IF:
-- - You need to revert during testing
-- - The new VIRTUAL columns are causing issues
-- - You want to compare performance between STORED vs VIRTUAL
--
-- WARNING: This will slow down UPDATE operations significantly under high load
--
-- HOW TO RUN:
--   mysql -u your_user -p your_database < sql/ROLLBACK_51_manual.sql
--
-- OR from MySQL prompt:
--   USE your_database;
--   SOURCE /path/to/Golbat/sql/ROLLBACK_51_manual.sql;
-- ============================================================================

USE `golbat_db`; -- Replace with your actual database name

-- Step 1: Drop VIRTUAL columns
ALTER TABLE `pokestop`
    DROP COLUMN IF EXISTS `quest_reward_type`,
    DROP COLUMN IF EXISTS `quest_item_id`,
    DROP COLUMN IF EXISTS `quest_reward_amount`,
    DROP COLUMN IF EXISTS `quest_pokemon_id`,
    DROP COLUMN IF EXISTS `alternative_quest_pokemon_id`,
    DROP COLUMN IF EXISTS `alternative_quest_reward_type`,
    DROP COLUMN IF EXISTS `alternative_quest_item_id`,
    DROP COLUMN IF EXISTS `alternative_quest_reward_amount`;

SELECT 'Step 1 complete: Dropped VIRTUAL columns' AS Status;

-- Step 2: Recreate as STORED columns (original behavior)
ALTER TABLE `pokestop`
    ADD COLUMN `quest_reward_type` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].type'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `quest_item_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.item_id'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `quest_reward_amount` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.amount'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `quest_pokemon_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`quest_rewards`,_utf8mb4'$[*].info.pokemon_id'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `alternative_quest_pokemon_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.pokemon_id'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `alternative_quest_reward_type` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].type'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `alternative_quest_item_id` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.item_id'),_utf8mb4'$[0]')) STORED,
    ADD COLUMN `alternative_quest_reward_amount` smallint unsigned 
        GENERATED ALWAYS AS (json_extract(json_extract(`alternative_quest_rewards`,_utf8mb4'$[*].info.amount'),_utf8mb4'$[0]')) STORED;

SELECT 'Step 2 complete: Recreated STORED columns' AS Status;

-- Step 3: Verify columns exist and are STORED
SELECT 
    COLUMN_NAME,
    GENERATION_EXPRESSION,
    EXTRA
FROM 
    INFORMATION_SCHEMA.COLUMNS
WHERE 
    TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'pokestop'
    AND COLUMN_NAME LIKE '%quest%'
    AND GENERATION_EXPRESSION IS NOT NULL
ORDER BY 
    ORDINAL_POSITION;

SELECT 'Rollback complete! Columns are now STORED (original behavior)' AS Status;
SELECT 'WARNING: UPDATE queries will be slower under high load' AS Warning;

-- ============================================================================
-- TO RE-APPLY THE OPTIMIZATION (go back to VIRTUAL):
--   Just restart Golbat - migration 51 will run automatically
--   OR manually run: sql/51_pokestop_generated_columns_virtual.up.sql
-- ============================================================================

