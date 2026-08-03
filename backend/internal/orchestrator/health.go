package orchestrator

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultRecoverySuccesses = 3

type HealthProbe interface {
	Reachable(context.Context, string) bool
}

type GroupHealth struct {
	Group             string    `json:"group"`
	WANReachable      bool      `json:"wan_reachable"`
	LANReachable      bool      `json:"lan_reachable"`
	Unavailable       bool      `json:"unavailable"`
	RecoverySuccesses int       `json:"recovery_successes"`
	RequiredSuccesses int       `json:"required_successes"`
	ObservedAt        time.Time `json:"observed_at"`
}

type groupHealthState struct {
	known       bool
	unavailable bool
	successes   int
}

type HealthTracker struct {
	mu                sync.Mutex
	states            map[string]groupHealthState
	recoverySuccesses int
	now               func() time.Time
}

func NewHealthTracker(recoverySuccesses int, now func() time.Time) *HealthTracker {
	if recoverySuccesses <= 0 {
		recoverySuccesses = DefaultRecoverySuccesses
	}
	if now == nil {
		now = time.Now
	}
	return &HealthTracker{states: map[string]groupHealthState{}, recoverySuccesses: recoverySuccesses, now: now}
}

func (tracker *HealthTracker) Evaluate(ctx context.Context, bindings []ServiceChainBindingInput, probe HealthProbe) (map[string]bool, []GroupHealth) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.states == nil {
		tracker.states = map[string]groupHealthState{}
	}
	reports := make([]GroupHealth, 0, len(bindings))
	unavailable := map[string]bool{}
	seen := map[string]bool{}
	for _, binding := range bindings {
		group := strings.TrimSpace(binding.Group)
		if group == "" || seen[group] {
			continue
		}
		seen[group] = true
		wanOK := probe != nil && probe.Reachable(ctx, strings.TrimSpace(binding.WANFacingNextHop))
		lanOK := probe != nil && probe.Reachable(ctx, strings.TrimSpace(binding.LANFacingNextHop))
		bothOK := wanOK && lanOK
		state := tracker.states[group]
		switch {
		case !state.known:
			state.known = true
			state.unavailable = !bothOK
			if bothOK {
				state.successes = tracker.recoverySuccesses
			}
		case !bothOK:
			state.unavailable = true
			state.successes = 0
		case state.unavailable:
			state.successes++
			if state.successes >= tracker.recoverySuccesses {
				state.unavailable = false
			}
		default:
			state.successes = tracker.recoverySuccesses
		}
		tracker.states[group] = state
		if state.unavailable {
			unavailable[group] = true
		}
		reports = append(reports, GroupHealth{Group: group, WANReachable: wanOK, LANReachable: lanOK, Unavailable: state.unavailable, RecoverySuccesses: state.successes, RequiredSuccesses: tracker.recoverySuccesses, ObservedAt: tracker.now().UTC()})
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Group < reports[j].Group })
	return unavailable, reports
}
