ALTER TABLE gym
    add `raid_seed` varchar(25) DEFAULT NULL AFTER `updated`;

ALTER TABLE pokestop
    add `quest_seed` varchar(25) DEFAULT NULL AFTER `enabled`;
ALTER TABLE pokestop
    add `alternative_quest_seed` varchar(25) DEFAULT NULL AFTER `power_up_end_timestamp`;