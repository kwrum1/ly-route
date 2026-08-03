package vpp

import "strings"

func abfTableFibIndex(results []VPPCTLCommandResult, via string) string {
	fields := strings.Fields(via)
	if len(fields) != 2 || fields[0] != "table" {
		return ""
	}
	output, err := commandOutput(results, "show ip fib table "+fields[1])
	if err != nil {
		return ""
	}
	lines := nonBlankLines(output)
	if len(lines) == 0 {
		return ""
	}
	for _, field := range strings.Fields(lines[0]) {
		if strings.HasPrefix(field, "fib_index:") {
			return strings.TrimSuffix(strings.TrimPrefix(field, "fib_index:"), ",")
		}
	}
	return ""
}
