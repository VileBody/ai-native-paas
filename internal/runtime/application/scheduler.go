package application

import (
	"sort"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type DeterministicScheduler struct{}

func (DeterministicScheduler) Select(cells []domain.RuntimeCell, region string, isolation runtimev1.IsolationClass, units int) (domain.RuntimeCell, error) {
	candidates := append([]domain.RuntimeCell(nil), cells...)
	sort.Slice(candidates, func(i, j int) bool {
		leftFree := candidates[i].CapacityUnits - candidates[i].AllocatedUnits
		rightFree := candidates[j].CapacityUnits - candidates[j].AllocatedUnits
		if leftFree != rightFree {
			return leftFree > rightFree
		}
		return candidates[i].ID < candidates[j].ID
	})
	for _, cell := range candidates {
		if cell.CanPlace(region, isolation, units) {
			return cell, nil
		}
	}
	return domain.RuntimeCell{}, domain.NewError(domain.CodeCapacity, "no compatible runtime cell has sufficient capacity")
}

var _ PlacementScheduler = DeterministicScheduler{}
