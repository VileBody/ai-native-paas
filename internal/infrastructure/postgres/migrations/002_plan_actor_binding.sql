ALTER TABLE infrastructure.plans
    ADD COLUMN IF NOT EXISTS requested_by_actor_id text;

UPDATE infrastructure.plans
SET requested_by_actor_id = 'migration-system'
WHERE requested_by_actor_id IS NULL;

ALTER TABLE infrastructure.plans
    ALTER COLUMN requested_by_actor_id SET NOT NULL;
