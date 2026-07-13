package domain

import "time"

type IdempotencyRecord struct {
	TenantID, Scope, Key, RequestHash, ResourceID string
	CreatedAt                                     time.Time
}
type OutboxRecord struct {
	ID, TenantID, Topic, AggregateID string
	Payload                          []byte
	CreatedAt                        time.Time
}
type AuditRecord struct {
	ID, TenantID, ActorID, Action, ResourceType, ResourceID string
	Data                                                    []byte
	CreatedAt                                               time.Time
}
type GitOpsCommitRecord struct {
	ID, TenantID, CellID, ReleaseID, DeploymentID, Path, ManifestHash, CommitSHA string
	CreatedAt                                                                    time.Time
}
type QuarantineRecord struct {
	ID, CellID, Namespace, Kind, Name, Reason string
	ObservedAt                                time.Time
}
type PlacementMigration struct {
	ID, TenantID, EnvironmentID, FromPlacementID, TargetCellID, State string
	CreatedAt                                                         time.Time
}
