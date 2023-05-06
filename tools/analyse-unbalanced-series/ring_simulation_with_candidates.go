package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	tokdistr "github.com/grafana/mimir/tools/analyse-unbalanced-series/tokendistributor"
)

const (
	maxTokenValue        = tokdistr.Token(math.MaxUint32)
	initialInstanceCount = 66
	iterations           = 10
)

func generateRingWithZoneAwareness(logger log.Logger, numTokensPerInstanceScenarios []int, replicationFactor, instancesPerZone int, zones []tokdistr.Zone, seedGeneratorProvider func(zones []tokdistr.Zone, replicationFactor, tokensPerInstance int, maxTokenValue tokdistr.Token) tokdistr.SeedGenerator) error {
	result := make([][]string, 0, instancesPerZone*len(zones))
	optimalTokenOwnership := float64(maxTokenValue) * float64(replicationFactor) / float64(instancesPerZone*len(zones))
	for it := 0; it < instancesPerZone*len(zones); it++ {
		result = append(result, make([]string, 0, iterations*len(numTokensPerInstanceScenarios)+2))
		result[it] = append(result[it], "")
		result[it] = append(result[it], fmt.Sprintf("%f", optimalTokenOwnership))
	}

	for _, numTokensPerInstance := range numTokensPerInstanceScenarios {
		for it := 0; it < iterations; it++ {
			level.Info(logger).Log("tokensPerInstance", numTokensPerInstance, "iteration", it)
			replicationStrategy := tokdistr.NewZoneAwareReplicationStrategy(replicationFactor, make(map[tokdistr.Instance]tokdistr.Zone, initialInstanceCount), nil, nil)
			tokenDistributor := tokdistr.NewTokenDistributor(numTokensPerInstance, len(zones), maxTokenValue, replicationStrategy, seedGeneratorProvider(zones, replicationFactor, numTokensPerInstance, maxTokenValue))
			var ownershipInfo *tokdistr.OwnershipInfo
			for i := 0; i < instancesPerZone; i++ {
				for j := 0; j < len(zones); j++ {
					instance := tokdistr.Instance(fmt.Sprintf("%s-%d", string(rune('A'+j)), i))
					_, _, ownershipInfo, _ = tokenDistributor.AddInstance(instance, zones[j])
					result[i*len(zones)+j][0] = string(instance)
				}
			}
			for i := 0; i < instancesPerZone*len(zones); i++ {
				result[i] = append(result[i], fmt.Sprintf("%f", ownershipInfo.InstanceOwnershipMap[tokdistr.Instance(result[i][0])]))
			}
		}
	}

	//Generate CSV header.
	csvHeader := make([]string, 0, iterations*len(numTokensPerInstanceScenarios)+1)
	csvHeader = append(csvHeader, "instance")
	csvHeader = append(csvHeader, "optimal ownership")
	for _, numTokensPerInstance := range numTokensPerInstanceScenarios {
		for i := 0; i < iterations; i++ {
			csvHeader = append(csvHeader, fmt.Sprintf("it:%d-tokens:%d", i+1, numTokensPerInstance))
		}
	}

	// Write result to CSV.
	w := newCSVWriter[[]string]()
	w.setHeader(csvHeader)
	w.setData(result, func(entry []string) []string {
		return entry
	})
	output := fmt.Sprintf("%s-simulated-ring-with-different-tokens-per-instance-with-candidates-selection-rf-%d-zone-awareness-%s.csv", time.Now().Local(), replicationFactor, formatEnabled(len(zones) > 1))
	filename := filepath.Join("tools", "analyse-unbalanced-series", "tokendistributor", output)
	if err := w.writeCSV(filename); err != nil {
		return err
	}
	return nil
}

func generateRingWithoutZoneAwareness(logger log.Logger, numTokensPerInstanceScenarios []int, replicationFactor, instancesCount int, seedGeneratorProvider func(zones []tokdistr.Zone, replicationFactor int, tokensPerInstance int, maxTokenValue tokdistr.Token) tokdistr.SeedGenerator) error {
	result := make([][]string, 0, instancesCount)
	optimalTokenOwnership := float64(maxTokenValue) * float64(replicationFactor) / float64(instancesCount)
	for it := 0; it < instancesCount; it++ {
		result = append(result, make([]string, 0, iterations*len(numTokensPerInstanceScenarios)+2))
		result[it] = append(result[it], "")
		result[it] = append(result[it], fmt.Sprintf("%f", optimalTokenOwnership))
	}

	zones := []tokdistr.Zone{tokdistr.SingleZone}

	for _, numTokensPerInstance := range numTokensPerInstanceScenarios {
		for it := 0; it < iterations; it++ {
			level.Info(logger).Log("tokensPerInstance", numTokensPerInstance, "iteration", it)
			replicationStrategy := tokdistr.NewSimpleReplicationStrategy(replicationFactor, nil)
			tokenDistributor := tokdistr.NewTokenDistributor(numTokensPerInstance, len(zones), maxTokenValue, replicationStrategy, seedGeneratorProvider(zones, replicationFactor, numTokensPerInstance, maxTokenValue))
			var ownershipInfo *tokdistr.OwnershipInfo
			for i := 0; i < instancesCount; i++ {
				instance := tokdistr.Instance(fmt.Sprintf("I-%d", i))
				_, _, ownershipInfo, _ = tokenDistributor.AddInstance(instance, zones[0])
				result[i][0] = string(instance)
			}
			for i := 0; i < instancesCount; i++ {
				result[i] = append(result[i], fmt.Sprintf("%f", ownershipInfo.InstanceOwnershipMap[tokdistr.Instance(result[i][0])]))
			}
		}
	}

	//Generate CSV header.
	csvHeader := make([]string, 0, iterations*len(numTokensPerInstanceScenarios)+1)
	csvHeader = append(csvHeader, "instance")
	csvHeader = append(csvHeader, "optimal ownership")
	for _, numTokensPerInstance := range numTokensPerInstanceScenarios {
		for i := 0; i < iterations; i++ {
			csvHeader = append(csvHeader, fmt.Sprintf("it:%d-tokens:%d", i+1, numTokensPerInstance))
		}
	}

	// Write result to CSV.
	w := newCSVWriter[[]string]()
	w.setHeader(csvHeader)
	w.setData(result, func(entry []string) []string {
		return entry
	})
	output := fmt.Sprintf("%s-simulated-ring-with-different-tokens-per-instance-with-candidates-selection-rf-%d-zone-awareness-%s.csv", time.Now().Local(), replicationFactor, formatEnabled(len(zones) > 1))
	filename := filepath.Join("tools", "analyse-unbalanced-series", "tokendistributor", output)
	if err := w.writeCSV(filename); err != nil {
		return err
	}
	return nil
}

func main() {
	logger := log.NewLogfmtLogger(os.Stdout)
	level.Info(logger).Log("msg", "Generating ring with the best candidate approach")
	numTokensPerInstanceScenarios := []int{4, 16, 64, 128, 256, 512}
	replicationFactor := 3
	//zones := []tokdistr.Zone{tokdistr.Zone("zone-a"), tokdistr.Zone("zone-b"), tokdistr.Zone("zone-c")}
	//instancesPerZone := initialInstanceCount / len(zones)
	seedGenerator := func(zones []tokdistr.Zone, replicationFactor, tokensPerInstance int, maxTokenValue tokdistr.Token) tokdistr.SeedGenerator {
		return tokdistr.NewPerfectlySpacedSeedGenerator(zones, replicationFactor, tokensPerInstance, maxTokenValue)
	}

	// generate ring with different tokens per instance with RF 1 and zone-awareness disabled
	generateRingWithoutZoneAwareness(logger, numTokensPerInstanceScenarios, 1, initialInstanceCount, seedGenerator)
	// generate ring with different tokens per instance with replication and zone-awareness enabled
	generateRingWithoutZoneAwareness(logger, numTokensPerInstanceScenarios, replicationFactor, initialInstanceCount, seedGenerator)
	// generate ring with different tokens per instance with replication and zone-awareness enabled
	//generateRingWithZoneAwareness(logger, numTokensPerInstanceScenarios, replicationFactor, instancesPerZone, zones, seedGenerator)
}
