package pivot_test

import (
	"context"
	"testing"
)

func TestCost_TofuPlanProducesDeterministicNormalizedEstimate(t *testing.T) {
	service, _, _ := infrastructureFixture()
	first, err := service.Plan(context.Background(), planCommand("estimate-a", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	reordered := `{"resource_changes":[
    {"type":"unknown_cache","provider_name":"example/unknown","address":"unknown_cache.app","change":{"actions":["create"]}},
    {"type":"twc_server","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","address":"twc_server.app","change":{"after":{"different":"ignored"},"actions":["create"]}}
  ],"terraform_version":"1.12.4","format_version":"1.2"}`
	second, err := service.Plan(context.Background(), planCommand("estimate-b", "staging", reordered))
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.PlanHash != second.Summary.PlanHash || first.Estimate.Version != second.Estimate.Version {
		t.Fatalf("normalized plan/estimate changed with input ordering: first=%#v second=%#v", first, second)
	}
	if first.Estimate.Minimum.MinorUnit != 125 || first.Estimate.Maximum.MinorUnit != 1_250_125 || !first.Estimate.ApprovalRequired {
		t.Fatalf("unexpected conservative 25%% estimate: %#v", first.Estimate)
	}
	if len(first.Estimate.Lines) != 2 || first.Estimate.Lines[0].Meter != "timeweb.server.month" || first.Estimate.Lines[1].PriceKnown {
		t.Fatalf("estimate lines are not canonical/provider-aware: %#v", first.Estimate.Lines)
	}
}
