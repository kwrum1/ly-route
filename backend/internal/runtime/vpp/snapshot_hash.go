package vpp

import "fmt"

func CanonicalSnapshotHash(snapshot Snapshot) (string, error) {
	return snapshotHash(snapshot)
}

func VerifySnapshotHash(snapshot Snapshot) error {
	if snapshot.Hash == "" {
		return fmt.Errorf("%w: snapshot hash is missing", ErrSnapshotIncomplete)
	}
	want, err := snapshotHash(snapshot)
	if err != nil {
		return err
	}
	if snapshot.Hash != want {
		return fmt.Errorf("%w: snapshot hash mismatch", ErrSnapshotIncomplete)
	}
	return nil
}
