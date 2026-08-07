package vpp

func appendBondDeleteState(existing []BondState, ids []string, prior []BondState) []BondState {
	states := append([]BondState(nil), existing...)
	for _, id := range ids {
		if _, found := bondStateByName(states, id); found {
			continue
		}
		if state, found := bondStateByName(prior, id); found {
			states = append(states, state)
		}
	}
	return states
}

func bondStateByName(states []BondState, name string) (BondState, bool) {
	for _, state := range states {
		if state.Name == name {
			return state, true
		}
	}
	return BondState{}, false
}
