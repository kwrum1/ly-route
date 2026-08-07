package vpp

import "strconv"

func validInterfaceAddressMetadata(fields []string) bool {
	if len(fields) == 0 {
		return true
	}
	if len(fields) != 5 || (fields[0] != "ip4" && fields[0] != "ip6") || fields[1] != "table-id" || fields[3] != "fib-idx" {
		return false
	}
	tableID, tableErr := strconv.Atoi(fields[2])
	fibIndex, fibErr := strconv.Atoi(fields[4])
	return tableErr == nil && fibErr == nil && tableID >= 0 && fibIndex >= 0
}
