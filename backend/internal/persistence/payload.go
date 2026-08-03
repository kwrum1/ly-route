package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrPayloadIntegrity = errors.New("persistence payload integrity check failed")

func VerifyPayload(payload []byte, expectedHash string) error {
	digest := sha256.Sum256(payload)
	actual := hex.EncodeToString(digest[:])
	if actual != expectedHash {
		return fmt.Errorf("%w: expected %s, got %s", ErrPayloadIntegrity, expectedHash, actual)
	}
	return nil
}
