package vpp

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func tapBridgeInterfaceNames(linuxInterface string) (string, string) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(linuxInterface))))
	token := fmt.Sprintf("%x", digest[:4])
	return "lrbr-" + token, "lrh-" + token
}
