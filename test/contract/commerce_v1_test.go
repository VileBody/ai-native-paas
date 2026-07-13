package contract_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func TestCommerceV1_PublicMoneyContractsContainNoFloatingPointFields(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(commercev1.Price{}), reflect.TypeOf(commercev1.PlanSpec{}),
		reflect.TypeOf(commercev1.UsageEvent{}), reflect.TypeOf(commercev1.RatedUsage{}),
		reflect.TypeOf(commercev1.InvoiceLine{}), reflect.TypeOf(commercev1.InvoicePreview{}),
		reflect.TypeOf(commercev1.QuotaRequest{}), reflect.TypeOf(commercev1.QuotaReservation{}),
	}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type, string)
	visit = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() == reflect.Map {
			visit(typ.Key(), path+".<key>")
			visit(typ.Elem(), path+".<value>")
			return
		}
		if typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64 {
			t.Fatalf("floating point field at %s (%s)", path, typ)
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			visit(field.Type, path+"."+field.Name)
		}
	}
	for _, typ := range types {
		visit(typ, typ.String())
	}
}

func TestCommerceV1_PriceJSONIsIntegerAndStable(t *testing.T) {
	raw, err := json.Marshal(commercev1.Price{MinorUnits: 7, PerQuantity: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"minor_units":7,"per_quantity":3600}` {
		t.Fatalf("json=%s", raw)
	}
}

func TestCommerceV1_MeterCatalogIsUniqueAndValid(t *testing.T) {
	catalog := commercev1.MeterCatalog()
	if len(catalog) == 0 {
		t.Fatal("empty meter catalog")
	}
	seen := map[commercev1.Meter]struct{}{}
	for _, meter := range catalog {
		if _, ok := seen[meter]; ok {
			t.Fatalf("duplicate meter %s", meter)
		}
		seen[meter] = struct{}{}
		if !commercev1.ValidMeter(meter) {
			t.Fatalf("catalog meter rejected: %s", meter)
		}
	}
	ordered := append([]commercev1.Meter(nil), catalog...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1] == ordered[i] {
			t.Fatalf("duplicate meter %s", ordered[i])
		}
	}
}

func TestCommerceV1_InvoicePreviewNormalizationIsDeterministic(t *testing.T) {
	input := commercev1.InvoicePreview{Lines: []commercev1.InvoiceLine{
		{ResourceType: "build", ResourceID: "b", Meter: commercev1.MeterBuildCPUSeconds, Kind: commercev1.UsageStandard},
		{ResourceType: "application", ResourceID: "a", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageCorrection},
		{ResourceType: "application", ResourceID: "a", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard},
	}}
	input.Normalize()
	raw1, _ := json.Marshal(input)
	input.Normalize()
	raw2, _ := json.Marshal(input)
	if string(raw1) != string(raw2) {
		t.Fatalf("normalization is not idempotent\n%s\n%s", raw1, raw2)
	}
}
