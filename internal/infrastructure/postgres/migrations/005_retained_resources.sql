ALTER TABLE infrastructure.plans
    ADD COLUMN IF NOT EXISTS retained_resources jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(retained_resources) = 'array');

ALTER TABLE infrastructure.plan_receipts
    ADD COLUMN IF NOT EXISTS retained_resources jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(retained_resources) = 'array');
