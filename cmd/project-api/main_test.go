package main

import "testing"

func TestDependencyReadinessIsFailClosedAndExplicit(t *testing.T) {
	t.Setenv("WORKSPACE_NAT_READY", "")
	if !dependencyUnavailable("WORKSPACE_NAT_READY") {
		t.Fatal("unset dependency unexpectedly enabled mutations")
	}
	t.Setenv("WORKSPACE_NAT_READY", "definitely")
	if !dependencyUnavailable("WORKSPACE_NAT_READY") {
		t.Fatal("malformed readiness unexpectedly enabled mutations")
	}
	t.Setenv("WORKSPACE_NAT_READY", "false")
	if !dependencyUnavailable("WORKSPACE_NAT_READY") {
		t.Fatal("false readiness unexpectedly enabled mutations")
	}
	t.Setenv("WORKSPACE_NAT_READY", "true")
	if dependencyUnavailable("WORKSPACE_NAT_READY") {
		t.Fatal("explicit true readiness did not unlock dependency")
	}
}
