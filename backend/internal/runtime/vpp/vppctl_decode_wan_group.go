package vpp

import (
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func configuredWANPathListMatches(candidate trafficpolicy.WANGroup, lists [][]fibPath) bool {
	for _, list := range lists {
		if len(list) != len(candidate.Members) {
			continue
		}
		matched := make([]bool, len(list))
		all := true
		for index, member := range candidate.Members {
			expected := candidate.Paths[member]
			expectedVia := strings.TrimSpace(expected.VPPInterface)
			if expectedVia == "" {
				expectedVia = member
			}
			expectedWeight := candidate.Weights[member]
			if expectedWeight < 1 || candidate.Mode == trafficpolicy.WANGroupPrimaryBackup {
				expectedWeight = 1
			}
			expectedPreference := 0
			if candidate.Mode == trafficpolicy.WANGroupPrimaryBackup {
				expectedPreference = index
			}
			found := false
			for pathIndex, path := range list {
				if matched[pathIndex] || path.weight != expectedWeight || path.preference != expectedPreference {
					continue
				}
				if strings.Contains(path.via, expectedVia) && (expected.NextHop == "" || strings.Contains(path.via, expected.NextHop)) {
					matched[pathIndex] = true
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
