package trafficpolicy

import (
	"fmt"
	"strings"
)

func compileWANGroupMembers(item map[string]any) ([]string, error) {
	members := []string{}
	for _, field := range []string{"wan_members", "members"} {
		fieldMembers := wanMemberIDs(item[field])
		seen := make(map[string]struct{}, len(fieldMembers))
		for _, member := range fieldMembers {
			if _, exists := seen[member]; exists {
				return nil, fmt.Errorf("duplicate member %q in %s", member, field)
			}
			seen[member] = struct{}{}
			members = appendUnique(members, member)
		}
	}
	return members, nil
}

func compilePrimaryBackupMembers(item map[string]any, members []string) ([]string, string, string, error) {
	primary := wanGroupSelectedMember(item, "primary_member", "primary_wan", "primary")
	backup := wanGroupSelectedMember(item, "backup_member", "backup_wan", "backup")
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member] = struct{}{}
	}
	if primary != "" {
		if _, exists := memberSet[primary]; !exists {
			return nil, "", "", fmt.Errorf("primary member %q is not a group member", primary)
		}
	} else {
		primary = members[0]
	}
	if backup != "" {
		if _, exists := memberSet[backup]; !exists {
			return nil, "", "", fmt.Errorf("backup member %q is not a group member", backup)
		}
	} else {
		for _, member := range members {
			if member != primary {
				backup = member
				break
			}
		}
	}
	if primary == backup {
		return nil, "", "", fmt.Errorf("primary member and backup member must be different")
	}

	ordered := []string{primary, backup}
	for _, member := range members {
		if member != primary && member != backup {
			ordered = append(ordered, member)
		}
	}
	return ordered, primary, backup, nil
}

func wanGroupSelectedMember(item map[string]any, keys ...string) string {
	if member := stringValue(item, keys...); member != "" {
		return strings.TrimSpace(member)
	}
	if loadBalance, ok := item["load_balance"].(map[string]any); ok {
		return strings.TrimSpace(stringValue(loadBalance, keys...))
	}
	return ""
}
