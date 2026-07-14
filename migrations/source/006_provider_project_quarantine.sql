CREATE TABLE IF NOT EXISTS source.provider_project_quarantine (
    provider text NOT NULL,
    provider_namespace_id bigint NOT NULL CHECK (provider_namespace_id > 0),
    provider_project_id bigint NOT NULL CHECK (provider_project_id > 0),
    provider_path text NOT NULL CHECK (provider_path <> '' AND length(provider_path) <= 512),
    web_url text NOT NULL DEFAULT '' CHECK (length(web_url) <= 2048),
    candidate_repository_id text NOT NULL CHECK (candidate_repository_id <> ''),
    reason text NOT NULL CHECK (reason IN (
        'external_identity_mismatch',
        'managed_label_missing',
        'unbound_managed_project'
    )),
    external_identity_matched boolean NOT NULL,
    managed_label_present boolean NOT NULL,
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (provider, provider_project_id),
    CHECK (last_observed_at >= first_observed_at)
);

CREATE INDEX IF NOT EXISTS source_provider_project_quarantine_observed_idx
    ON source.provider_project_quarantine (last_observed_at, provider, provider_project_id);
