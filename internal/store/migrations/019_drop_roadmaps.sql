-- Drop roadmaps. The feature is withdrawn.
--
-- Four attempts at generation -- LLM-authored tree, then resilience patches,
-- then structure taken from the headings instead -- and none of them produced a
-- tree worth ticking off. Rather than carry a half-trusted surface, the whole
-- thing goes: package, endpoints, pages and tables.
--
-- 018_roadmaps.sql stays where it is. Migrations are append-only and already
-- recorded in schema_migrations, so undoing one means writing the next.

DROP TABLE IF EXISTS roadmap_sources;
DROP TABLE IF EXISTS roadmap_items;
DROP TABLE IF EXISTS roadmaps;

-- The feed rows outlive the tables they point at: SetItemState and Delete each
-- appended one, and no client will ever be able to resolve those ids again.
DELETE FROM change_log WHERE entity_type IN ('roadmap', 'roadmap_item');
