// Package config loads immutable, closed-schema infrastructure pricing snapshots.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
)

const Schema = "ai-native-paas.io/infrastructure-price-book/v1"

type document struct {
	Schema   string                   `json:"schema"`
	RateCard commercev2.RateCard      `json:"rate_card"`
	Prices   map[string]unitPriceJSON `json:"prices"`
}

type unitPriceJSON struct {
	Meter                    string `json:"meter"`
	Unit                     string `json:"unit"`
	ProviderMinorPerQuantity int64  `json:"provider_minor_per_quantity"`
	UnknownMaximumMinor      int64  `json:"unknown_maximum_minor,omitempty"`
	Known                    bool   `json:"known"`
}

func LoadPriceBook(path string) (infraapp.PriceBook, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return infraapp.PriceBook{}, errors.New("infrastructure price book file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return infraapp.PriceBook{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return infraapp.PriceBook{}, errors.New("invalid infrastructure price book size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value document
	if err := decoder.Decode(&value); err != nil {
		return infraapp.PriceBook{}, errors.New("invalid infrastructure price book JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || value.Schema != Schema {
		return infraapp.PriceBook{}, errors.New("invalid infrastructure price book schema")
	}
	book := infraapp.PriceBook{RateCard: value.RateCard, Prices: make(map[string]infraapp.UnitPrice, len(value.Prices))}
	for resourceType, price := range value.Prices {
		book.Prices[resourceType] = infraapp.UnitPrice{
			Meter: price.Meter, Unit: price.Unit,
			ProviderMinorPerQuantity: price.ProviderMinorPerQuantity,
			UnknownMaximumMinor:      price.UnknownMaximumMinor, Known: price.Known,
		}
	}
	if err := book.Validate(); err != nil {
		return infraapp.PriceBook{}, err
	}
	return book, nil
}
