package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPolicyRepositoryPersistsTypedPolicyAndProtectsTopologyReferences(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t, ctx)
	topology := validPolicyTopology(t)
	if err := repository.Replace(ctx, topology); err != nil {
		t.Fatal(err)
	}
	policy, err := ParsePolicy(topology, validPolicyInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReplacePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	stored, checksum, err := repository.PolicySnapshot(ctx)
	if err != nil || checksum == "" || !reflect.DeepEqual(stored.View(), policy.View()) {
		t.Fatalf("stored=%#v checksum=%q err=%v", stored.View(), checksum, err)
	}
	topologyChecksum, err := repository.Checksum(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteGroup(ctx, "inline-east"); !errors.Is(err, ErrDeletedPolicyReference) {
		t.Fatalf("delete referenced group error=%v", err)
	}
	after, err := repository.Checksum(ctx)
	if err != nil || after != topologyChecksum {
		t.Fatalf("topology changed after rejected delete: %q %v", after, err)
	}
	if err := repository.Delete(ctx); !errors.Is(err, ErrDeletedPolicyReference) {
		t.Fatalf("delete referenced topology error=%v", err)
	}
	if err := repository.DeletePolicy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteGroup(ctx, "inline-east"); err != nil {
		t.Fatal(err)
	}
}
