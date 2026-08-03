package orchestratorapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/runtime/vpp"
)

type transparentTestRuntime struct {
	applications []transparentApplication
	fail         error
	disables     int
	commits      int
}

type transparentApplication struct {
	topology orchestrator.TopologyView
	policy   *orchestrator.PolicyView
}

func (runtime *transparentTestRuntime) ApplyTransparent(_ context.Context, _ string, topology orchestrator.Topology, policy *orchestrator.Policy) error {
	if runtime.fail != nil {
		return runtime.fail
	}
	application := transparentApplication{topology: topology.View()}
	if policy != nil {
		view := policy.View()
		application.policy = &view
	}
	runtime.applications = append(runtime.applications, application)
	return nil
}

func (runtime *transparentTestRuntime) DisableTransparent(context.Context, string) error {
	if runtime.fail != nil {
		return runtime.fail
	}
	runtime.disables++
	return nil
}

func (runtime *transparentTestRuntime) CommitTransparentTransaction(context.Context) error {
	runtime.commits++
	return nil
}

func (*transparentTestRuntime) ApplyServiceChain(context.Context, string, orchestrator.ServiceChain, []vpp.NativeAttachment) (vpp.ServiceChainApplyResult, error) {
	return vpp.ServiceChainApplyResult{}, nil
}

func TestTransparentRuntimeAppliesTopologyAndWholePolicyBeforeSuccess(t *testing.T) {
	runtime := &transparentTestRuntime{}
	_, server := newTestHTTPServer(t, runtime)
	topology, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if response := sendRequest(t, server, http.MethodPut, TopologyPath, topology); response.StatusCode != http.StatusOK {
		t.Fatalf("topology: %d %s", response.StatusCode, response.Body)
	}
	if len(runtime.applications) != 1 || runtime.applications[0].policy != nil {
		t.Fatalf("topology applications = %#v", runtime.applications)
	}
	policy := []byte(`{"schema_version":1,"ip_objects":[],"policy_groups":[{"id":"default","position":10,"rules":[{"id":"all","sequence":10,"match":{"sources":["any"],"destinations":["any"],"protocol":"any"},"action":{"kind":"via","group":"inline-east"}}]}],"default":{"kind":"direct"}}`)
	response := sendRequest(t, server, http.MethodPut, PolicyPath, policy)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policy: %d %s", response.StatusCode, response.Body)
	}
	if len(runtime.applications) != 2 || runtime.applications[1].policy == nil || runtime.applications[1].policy.Groups[0].Rules[0].ID != "all" {
		t.Fatalf("policy applications = %#v", runtime.applications)
	}
	if runtime.commits != 2 {
		t.Fatalf("journal commits = %d, want topology and policy commits", runtime.commits)
	}
}

func TestTransparentRuntimeDoesNotExposeLegacyPerFlowServiceChainAPI(t *testing.T) {
	runtime := &transparentTestRuntime{}
	_, server := newTestHTTPServer(t, runtime)
	for _, path := range []string{ServiceChainCompilePath, ServiceChainApplyPath, ServiceChainStatusPath} {
		response := sendRequest(t, server, http.MethodPost, path, []byte(`{}`))
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("legacy route %s status = %d, want 404: %s", path, response.StatusCode, response.Body)
		}
	}
}

func TestTransparentRuntimeFailureDoesNotPersistPolicy(t *testing.T) {
	runtime := &transparentTestRuntime{}
	repository, server := newTestHTTPServer(t, runtime)
	topology, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if response := sendRequest(t, server, http.MethodPut, TopologyPath, topology); response.StatusCode != http.StatusOK {
		t.Fatalf("topology: %d %s", response.StatusCode, response.Body)
	}
	runtime.fail = errors.New("VPP semantic readback failed")
	policy := []byte(`{"schema_version":1,"ip_objects":[],"policy_groups":[{"id":"default","position":10,"rules":[{"id":"all","sequence":10,"match":{"sources":["any"],"destinations":["any"],"protocol":"any"},"action":{"kind":"direct"}}]}],"default":{"kind":"direct"}}`)
	response := sendRequest(t, server, http.MethodPut, PolicyPath, policy)
	if response.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(response.Body, []byte("VPP semantic readback failed")) {
		t.Fatalf("policy failure: %d %s", response.StatusCode, response.Body)
	}
	if _, _, err := repository.PolicySnapshot(context.Background()); !errors.Is(err, orchestrator.ErrPolicyNotFound) {
		t.Fatalf("policy persisted after runtime failure: %v", err)
	}
}

func TestTransparentRuntimeDisablesBeforeTopologyDelete(t *testing.T) {
	runtime := &transparentTestRuntime{}
	repository, server := newTestHTTPServer(t, runtime)
	topology, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if response := sendRequest(t, server, http.MethodPut, TopologyPath, topology); response.StatusCode != http.StatusOK {
		t.Fatalf("topology: %d %s", response.StatusCode, response.Body)
	}
	if response := sendRequest(t, server, http.MethodDelete, TopologyPath, nil); response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete topology: %d %s", response.StatusCode, response.Body)
	}
	if runtime.disables != 1 {
		t.Fatalf("disable calls = %d, want 1", runtime.disables)
	}
	if runtime.commits != 2 {
		t.Fatalf("journal commits = %d, want topology apply and delete commits", runtime.commits)
	}
	if _, _, err := repository.Snapshot(context.Background()); !errors.Is(err, orchestrator.ErrTopologyNotFound) {
		t.Fatalf("topology remains after disable: %v", err)
	}
}

func TestTransparentRuntimeDisableFailurePreservesTopology(t *testing.T) {
	runtime := &transparentTestRuntime{}
	repository, server := newTestHTTPServer(t, runtime)
	topology, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if response := sendRequest(t, server, http.MethodPut, TopologyPath, topology); response.StatusCode != http.StatusOK {
		t.Fatalf("topology: %d %s", response.StatusCode, response.Body)
	}
	runtime.fail = errors.New("VPP failed to lock")
	response := sendRequest(t, server, http.MethodDelete, TopologyPath, nil)
	if response.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(response.Body, []byte("VPP failed to lock")) {
		t.Fatalf("delete failure: %d %s", response.StatusCode, response.Body)
	}
	if _, _, err := repository.Snapshot(context.Background()); err != nil {
		t.Fatalf("topology deleted after disable failure: %v", err)
	}
}
