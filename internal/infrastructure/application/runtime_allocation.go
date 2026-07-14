package application

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"

	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	"gopkg.in/yaml.v3"
)

const maxRenderedResources = 4096

var kubernetesQuantity = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?|\.[0-9]+)([eE][+-]?[0-9]+|[numkKMGTPE]i?)?$`)

type quantityValue string

func (q *quantityValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!float") {
		return errors.New("resource quantity must be a scalar")
	}
	*q = quantityValue(node.Value)
	return nil
}

type renderedResource struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   renderedMetadata   `yaml:"metadata"`
	Items      []renderedResource `yaml:"items"`
	Spec       renderedSpec       `yaml:"spec"`
}

type renderedMetadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type renderedSpec struct {
	Replicas       *int64                      `yaml:"replicas"`
	Parallelism    *int64                      `yaml:"parallelism"`
	Type           string                      `yaml:"type"`
	Template       renderedPodTemplate         `yaml:"template"`
	Containers     []renderedContainer         `yaml:"containers"`
	InitContainers []renderedContainer         `yaml:"initContainers"`
	Overhead       map[string]quantityValue    `yaml:"overhead"`
	Resources      renderedPersistentClaimSpec `yaml:"resources"`
	JobTemplate    renderedCronJobTemplate     `yaml:"jobTemplate"`
}

type renderedPodTemplate struct {
	Spec renderedPodSpec `yaml:"spec"`
}

type renderedPodSpec struct {
	Containers     []renderedContainer      `yaml:"containers"`
	InitContainers []renderedContainer      `yaml:"initContainers"`
	Overhead       map[string]quantityValue `yaml:"overhead"`
}

type renderedContainer struct {
	Resources renderedContainerResources `yaml:"resources"`
}

type renderedContainerResources struct {
	Requests map[string]quantityValue `yaml:"requests"`
}

type renderedPersistentClaimSpec struct {
	Requests map[string]quantityValue `yaml:"requests"`
}

type renderedCronJobTemplate struct {
	Spec struct {
		Parallelism *int64              `yaml:"parallelism"`
		Template    renderedPodTemplate `yaml:"template"`
	} `yaml:"spec"`
}

func normalizeRequestedRuntimeAllocation(raw []byte) (commercev2.RequestedRuntimeAllocation, error) {
	var result commercev2.RequestedRuntimeAllocation
	if len(bytes.TrimSpace(raw)) == 0 {
		return result, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	seen := make(map[string]struct{})
	count := 0
	for {
		var resource renderedResource
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, errors.New("invalid rendered Kubernetes manifests")
		}
		if strings.TrimSpace(resource.Kind) == "" && strings.TrimSpace(resource.APIVersion) == "" {
			continue
		}
		if err := accumulateRenderedResource(&result, resource, seen, &count); err != nil {
			return commercev2.RequestedRuntimeAllocation{}, err
		}
	}
	if err := result.Validate(); err != nil {
		return commercev2.RequestedRuntimeAllocation{}, err
	}
	return result, nil
}

func accumulateRenderedResource(total *commercev2.RequestedRuntimeAllocation, resource renderedResource, seen map[string]struct{}, count *int) error {
	(*count)++
	if *count > maxRenderedResources {
		return errors.New("too many rendered Kubernetes resources")
	}
	kind := strings.TrimSpace(resource.Kind)
	if kind == "List" {
		for _, item := range resource.Items {
			if err := accumulateRenderedResource(total, item, seen, count); err != nil {
				return err
			}
		}
		return nil
	}
	if resource.Metadata.Name != "" {
		identity := strings.Join([]string{resource.APIVersion, kind, resource.Metadata.Namespace, resource.Metadata.Name}, "\x00")
		if _, duplicate := seen[identity]; duplicate {
			return errors.New("duplicate rendered Kubernetes resource")
		}
		seen[identity] = struct{}{}
	}

	switch kind {
	case "Deployment", "StatefulSet", "ReplicaSet", "ReplicationController":
		replicas, err := cardinality(resource.Spec.Replicas)
		if err != nil {
			return err
		}
		requested, err := requestedByPod(resource.Spec.Template.Spec)
		if err != nil {
			return err
		}
		return addRequestedPods(total, requested, replicas)
	case "Job":
		parallelism, err := cardinality(resource.Spec.Parallelism)
		if err != nil {
			return err
		}
		requested, err := requestedByPod(resource.Spec.Template.Spec)
		if err != nil {
			return err
		}
		return addRequestedPods(total, requested, parallelism)
	case "Pod":
		requested, err := requestedByPod(renderedPodSpec{
			Containers: resource.Spec.Containers, InitContainers: resource.Spec.InitContainers, Overhead: resource.Spec.Overhead,
		})
		if err != nil {
			return err
		}
		return addRequestedPods(total, requested, 1)
	case "DaemonSet":
		requested, err := requestedByPod(resource.Spec.Template.Spec)
		if err != nil {
			return err
		}
		if requested.CPUMillicores != 0 || requested.MemoryMiB != 0 {
			return errors.New("DaemonSet runtime allocation requires an explicit node cardinality policy")
		}
	case "CronJob":
		requested, err := requestedByPod(resource.Spec.JobTemplate.Spec.Template.Spec)
		if err != nil {
			return err
		}
		if requested.CPUMillicores != 0 || requested.MemoryMiB != 0 {
			return errors.New("CronJob runtime allocation requires an explicit schedule policy")
		}
	case "PersistentVolumeClaim":
		storage, err := requestQuantity(resource.Spec.Resources.Requests, "storage", 1, 1<<20)
		if err != nil {
			return err
		}
		next, err := addNonNegative(total.StorageMiB, storage)
		if err != nil {
			return errors.New("runtime storage allocation overflow")
		}
		total.StorageMiB = next
	case "Service":
		if resource.Spec.Type == "LoadBalancer" {
			next, err := addNonNegative(total.LoadBalancers, 1)
			if err != nil {
				return errors.New("load balancer allocation overflow")
			}
			total.LoadBalancers = next
		}
	}
	return nil
}

func cardinality(value *int64) (int64, error) {
	if value == nil {
		return 1, nil
	}
	if *value < 0 {
		return 0, errors.New("negative Kubernetes workload cardinality")
	}
	return *value, nil
}

func requestedByPod(spec renderedPodSpec) (commercev2.RequestedRuntimeAllocation, error) {
	var regular commercev2.RequestedRuntimeAllocation
	for _, container := range spec.Containers {
		cpu, err := requestQuantity(container.Resources.Requests, "cpu", 1000, 1)
		if err != nil {
			return regular, err
		}
		memory, err := requestQuantity(container.Resources.Requests, "memory", 1, 1<<20)
		if err != nil {
			return regular, err
		}
		regular.CPUMillicores, err = addNonNegative(regular.CPUMillicores, cpu)
		if err != nil {
			return regular, errors.New("pod CPU allocation overflow")
		}
		regular.MemoryMiB, err = addNonNegative(regular.MemoryMiB, memory)
		if err != nil {
			return regular, errors.New("pod memory allocation overflow")
		}
	}
	var initMaximum commercev2.RequestedRuntimeAllocation
	for _, container := range spec.InitContainers {
		cpu, err := requestQuantity(container.Resources.Requests, "cpu", 1000, 1)
		if err != nil {
			return regular, err
		}
		memory, err := requestQuantity(container.Resources.Requests, "memory", 1, 1<<20)
		if err != nil {
			return regular, err
		}
		initMaximum.CPUMillicores = max64(initMaximum.CPUMillicores, cpu)
		initMaximum.MemoryMiB = max64(initMaximum.MemoryMiB, memory)
	}
	regular.CPUMillicores = max64(regular.CPUMillicores, initMaximum.CPUMillicores)
	regular.MemoryMiB = max64(regular.MemoryMiB, initMaximum.MemoryMiB)
	overheadCPU, err := requestQuantity(spec.Overhead, "cpu", 1000, 1)
	if err != nil {
		return regular, err
	}
	overheadMemory, err := requestQuantity(spec.Overhead, "memory", 1, 1<<20)
	if err != nil {
		return regular, err
	}
	regular.CPUMillicores, err = addNonNegative(regular.CPUMillicores, overheadCPU)
	if err != nil {
		return regular, errors.New("pod CPU overhead overflow")
	}
	regular.MemoryMiB, err = addNonNegative(regular.MemoryMiB, overheadMemory)
	if err != nil {
		return regular, errors.New("pod memory overhead overflow")
	}
	return regular, nil
}

func addRequestedPods(total *commercev2.RequestedRuntimeAllocation, requested commercev2.RequestedRuntimeAllocation, cardinality int64) error {
	cpu, err := multiplyNonNegative(requested.CPUMillicores, cardinality)
	if err != nil {
		return errors.New("runtime CPU allocation overflow")
	}
	memory, err := multiplyNonNegative(requested.MemoryMiB, cardinality)
	if err != nil {
		return errors.New("runtime memory allocation overflow")
	}
	total.CPUMillicores, err = addNonNegative(total.CPUMillicores, cpu)
	if err != nil {
		return errors.New("runtime CPU allocation overflow")
	}
	total.MemoryMiB, err = addNonNegative(total.MemoryMiB, memory)
	if err != nil {
		return errors.New("runtime memory allocation overflow")
	}
	return nil
}

func requestQuantity(requests map[string]quantityValue, resource string, targetNumerator, targetDenominator int64) (int64, error) {
	value, exists := requests[resource]
	if !exists || strings.TrimSpace(string(value)) == "" {
		return 0, nil
	}
	result, err := parseKubernetesQuantity(string(value), targetNumerator, targetDenominator)
	if err != nil {
		return 0, fmt.Errorf("invalid Kubernetes %s request", resource)
	}
	return result, nil
}

func parseKubernetesQuantity(raw string, targetNumerator, targetDenominator int64) (int64, error) {
	if targetNumerator <= 0 || targetDenominator <= 0 {
		return 0, errors.New("invalid quantity target")
	}
	parts := kubernetesQuantity.FindStringSubmatch(strings.TrimSpace(raw))
	if parts == nil {
		return 0, errors.New("invalid Kubernetes quantity")
	}
	value, ok := new(big.Rat).SetString(parts[1])
	if !ok || value.Sign() < 0 {
		return 0, errors.New("invalid Kubernetes quantity")
	}
	factor, err := quantityFactor(parts[2])
	if err != nil {
		return 0, err
	}
	value.Mul(value, factor)
	value.Mul(value, new(big.Rat).SetFrac(big.NewInt(targetNumerator), big.NewInt(targetDenominator)))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("Kubernetes quantity overflow")
	}
	return quotient.Int64(), nil
}

func quantityFactor(suffix string) (*big.Rat, error) {
	switch suffix {
	case "":
		return big.NewRat(1, 1), nil
	case "n":
		return big.NewRat(1, 1_000_000_000), nil
	case "u":
		return big.NewRat(1, 1_000_000), nil
	case "m":
		return big.NewRat(1, 1_000), nil
	case "k", "K":
		return big.NewRat(1_000, 1), nil
	case "M":
		return big.NewRat(1_000_000, 1), nil
	case "G":
		return big.NewRat(1_000_000_000, 1), nil
	case "T":
		return big.NewRat(1_000_000_000_000, 1), nil
	case "P":
		return big.NewRat(1_000_000_000_000_000, 1), nil
	case "E":
		return new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)), nil
	case "Ki", "Mi", "Gi", "Ti", "Pi", "Ei":
		power := map[string]int64{"Ki": 1, "Mi": 2, "Gi": 3, "Ti": 4, "Pi": 5, "Ei": 6}[suffix]
		return new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(1024), big.NewInt(power), nil)), nil
	}
	if len(suffix) >= 2 && (suffix[0] == 'e' || suffix[0] == 'E') {
		exponent := new(big.Int)
		if _, ok := exponent.SetString(suffix[1:], 10); !ok || !exponent.IsInt64() || exponent.Int64() < -63 || exponent.Int64() > 63 {
			return nil, errors.New("invalid Kubernetes quantity exponent")
		}
		power := new(big.Int).Exp(big.NewInt(10), big.NewInt(abs64(exponent.Int64())), nil)
		if exponent.Sign() < 0 {
			return new(big.Rat).SetFrac(big.NewInt(1), power), nil
		}
		return new(big.Rat).SetInt(power), nil
	}
	return nil, errors.New("invalid Kubernetes quantity suffix")
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
