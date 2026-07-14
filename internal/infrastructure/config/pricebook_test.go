package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPriceBook_RequiresClosedVersionedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	raw := `{"schema":"ai-native-paas.io/infrastructure-price-book/v1","rate_card":{"rate_card_id":"beta-default","version":"2026-07-14","markup_basis_points":2500,"currency":"RUB","price_snapshot_id":"timeweb-msk-2026-07-14"},"prices":{"twc_server":{"meter":"timeweb.server.month","unit":"server-month","provider_minor_per_quantity":100000,"known":true}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := LoadPriceBook(path)
	if err != nil || book.RateCard.MarkupBasisPoints != 2500 || !book.Prices["twc_server"].Known {
		t.Fatalf("book=%#v err=%v", book, err)
	}
	if err := os.WriteFile(path, []byte(raw[:len(raw)-1]+`,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPriceBook(path); err == nil {
		t.Fatal("unknown price book field was accepted")
	}
}
