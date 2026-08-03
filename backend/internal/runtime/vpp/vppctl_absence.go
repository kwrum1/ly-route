package vpp

import "strings"

func verifyVPPCTLAbsence(results []VPPCTLCommandResult, commands []string, kind, id string) error {
	for _, command := range commands {
		output, err := commandOutput(results, command)
		if err != nil {
			return err
		}
		value := strings.ToLower(strings.TrimSpace(output))
		if !strings.Contains(value, "not found") && !strings.Contains(value, "no such") && !strings.Contains(value, "not configured") && value != "empty" {
			return snapshotDecodeError("deleted %s %q remains or returned unknown absence grammar", kind, id)
		}
	}
	return nil
}
