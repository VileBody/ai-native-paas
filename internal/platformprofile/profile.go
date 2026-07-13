// Package platformprofile prevents development adapters from starting under a production profile.
package platformprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Profile string

const (
	Development Profile = "development"
	Test        Profile = "test"
	Production  Profile = "production"
)

type AdapterClass string

const (
	DevelopmentAdapter AdapterClass = "development"
	ProductionAdapter  AdapterClass = "production"
)

type Adapter struct {
	Name  string
	Class AdapterClass
}

func Dev(name string) Adapter  { return Adapter{Name: name, Class: DevelopmentAdapter} }
func Prod(name string) Adapter { return Adapter{Name: name, Class: ProductionAdapter} }

func Parse(value string) (Profile, error) {
	switch Profile(strings.ToLower(strings.TrimSpace(value))) {
	case "", Development:
		return Development, nil
	case Test:
		return Test, nil
	case Production:
		return Production, nil
	default:
		return "", errors.New("PLATFORM_PROFILE must be development, test, or production")
	}
}

func Validate(value, component string, adapters ...Adapter) (Profile, error) {
	profile, err := Parse(value)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(component) == "" || len(adapters) == 0 {
		return "", errors.New("component and adapter inventory are required")
	}
	var development []string
	for _, adapter := range adapters {
		if strings.TrimSpace(adapter.Name) == "" || (adapter.Class != DevelopmentAdapter && adapter.Class != ProductionAdapter) {
			return "", errors.New("adapter inventory is invalid")
		}
		if adapter.Class == DevelopmentAdapter {
			development = append(development, adapter.Name)
		}
	}
	if profile == Production && len(development) > 0 {
		sort.Strings(development)
		return "", fmt.Errorf("%s production profile rejects development adapters: %s", component, strings.Join(development, ", "))
	}
	return profile, nil
}
