// Package eval validates scenario selections before work starts and keeps report
// ordering independent from bounded worker scheduling.
package eval

import (
	"fmt"
	"sort"
)

// selectScenarios validates exact IDs and returns matching scenarios in suite
// declaration order.
func selectScenarios(scenarios []Scenario, ids []string) ([]Scenario, error) {
	wanted, err := selectorSet("scenario", ids)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		known[scenario.ID] = struct{}{}
	}
	if err := validateKnownSelectors("scenario", ids, known); err != nil {
		return nil, err
	}
	selected := make([]Scenario, 0, len(wanted))
	for _, scenario := range scenarios {
		if _, ok := wanted[scenario.ID]; ok {
			selected = append(selected, scenario)
		}
	}
	return selected, nil
}

// selectTags validates tags and returns matching scenarios in suite declaration
// order using any-tag matching.
func selectTags(scenarios []Scenario, tags []string) ([]Scenario, error) {
	wanted, err := selectorSet("tag", tags)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{})
	for _, scenario := range scenarios {
		for _, tag := range scenario.Tags {
			known[tag] = struct{}{}
		}
	}
	if err := validateKnownSelectors("tag", tags, known); err != nil {
		return nil, err
	}
	selected := make([]Scenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		for _, tag := range scenario.Tags {
			if _, ok := wanted[tag]; ok {
				selected = append(selected, scenario)
				break
			}
		}
	}
	return selected, nil
}

// selectorSet validates one explicit selector list before any evaluation work.
func selectorSet(kind string, values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("evaluation %s selection is empty", kind)
	}
	selected := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("evaluation %s is empty", kind)
		}
		if _, exists := selected[value]; exists {
			return nil, fmt.Errorf("duplicate evaluation %s %q", kind, value)
		}
		selected[value] = struct{}{}
	}
	return selected, nil
}

// validateKnownSelectors reports the first unknown value in caller order so
// invalid selections always produce the same diagnostic.
func validateKnownSelectors(kind string, values []string, known map[string]struct{}) error {
	for _, value := range values {
		if _, exists := known[value]; !exists {
			return fmt.Errorf("unknown evaluation %s %q", kind, value)
		}
	}
	return nil
}

// scenarioSchedule returns report-slot indexes in dispatch order. Serial runs
// retain declaration order; concurrent runs use stable longest-timeout-first
// scheduling while reports continue to use their original slots.
func scenarioSchedule(scenarios []Scenario, maxConcurrency int) []int {
	schedule := make([]int, len(scenarios))
	for index := range scenarios {
		schedule[index] = index
	}
	if maxConcurrency == 1 {
		return schedule
	}
	sort.SliceStable(schedule, func(left, right int) bool {
		return scenarios[schedule[left]].Timeout > scenarios[schedule[right]].Timeout
	})
	return schedule
}
