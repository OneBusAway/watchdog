-- New close reasons for unmatched-stop episodes whose agency disappears.
--
-- An episode closes against a complete response when its stop stops being
-- reported ('not_reported'). Two cases have no such response: OBA stops
-- listing the agency in agencyIDs, so the agency's responses are never
-- complete again ('agency_unlisted'), and Watchdog stops polling the agency
-- because it left the configuration ('agency_removed'). Both close the
-- episode against the agency's last complete response, so the existing
-- consistency constraint, the complete-response trigger, and the
-- investigation views keep working unchanged.

-- +goose Up
ALTER TABLE unmatched_stop_episode
    DROP CONSTRAINT unmatched_stop_episode_close_reason_check,
    ADD CONSTRAINT unmatched_stop_episode_close_reason_check
        CHECK (close_reason IN ('not_reported', 'agency_unlisted', 'agency_removed'));

-- +goose Down
ALTER TABLE unmatched_stop_episode
    DROP CONSTRAINT unmatched_stop_episode_close_reason_check,
    ADD CONSTRAINT unmatched_stop_episode_close_reason_check
        CHECK (close_reason IN ('not_reported'));
