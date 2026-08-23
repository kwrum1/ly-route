package proxy

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidSubscription = errors.New("invalid proxy subscription")

func CompileSubscription(subscription Subscription, nodes []Node) (XrayOutbound, error) {
	return CompileSubscriptionWithSelection(subscription, nodes, nil)
}

func CompileSubscriptionWithSelection(subscription Subscription, nodes []Node, probes []NodeProbe) (XrayOutbound, error) {
	if strings.TrimSpace(subscription.ID) == "" || strings.TrimSpace(subscription.URL) == "" {
		return XrayOutbound{}, fmt.Errorf("%w: id and URL are required", ErrInvalidSubscription)
	}
	if !subscription.Enabled {
		return XrayOutbound{}, fmt.Errorf("%w: subscription %q is disabled", ErrInvalidSubscription, subscription.ID)
	}
	active, err := SelectSubscriptionNode(subscription, nodes, probes)
	if err != nil {
		return XrayOutbound{}, err
	}
	activeID := strings.TrimSpace(active.ID)
	if strings.TrimSpace(active.Secret) == "" {
		return XrayOutbound{}, fmt.Errorf("%w: active node %q has no runtime credential", ErrInvalidSubscription, activeID)
	}
	outbound, err := CompileNodeOutbound(active)
	if err != nil {
		return XrayOutbound{}, fmt.Errorf("%w: compile active node %q: %v", ErrInvalidSubscription, activeID, err)
	}
	return outbound, nil
}
