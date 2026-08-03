package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/orchestratorapi"
	"ly-route/backend/internal/product"
)

func TestOrchestratorRepositoryRoutesShareMainAuthentication(t *testing.T) {
	server, store, cookie := productTestServer(t, product.Orchestrator())
	repository, err := orchestrator.NewRepository(store, orchestrator.RepositoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server.orchestratorRepository = repository

	unauthenticated := request(t, server, http.MethodGet, orchestratorapi.TopologyPath)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	authenticated := authenticatedJSONRequest(t, server, http.MethodGet, orchestratorapi.TopologyPath, "", cookie)
	if authenticated.Code != http.StatusNotFound {
		t.Fatalf("authenticated empty repository status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}
}

func TestRuntimeDataInterfacesIncludeConfirmedOrchestratorTopology(t *testing.T) {
	server, store, _ := productTestServer(t, product.Orchestrator())
	repository, err := orchestrator.NewRepository(store, orchestrator.RepositoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server.orchestratorRepository = repository

	input := orchestrator.TopologyInput{
		SchemaVersion:       orchestrator.SchemaVersion,
		ManagementInterface: "eth0",
		Interfaces: []orchestrator.InterfaceInput{
			{Name: "lan", Role: orchestrator.RoleLAN, Bond: &orchestrator.BondInput{Name: "bond-lan", Members: []string{"eth1", "eth2"}}},
			{Name: "wan", Role: orchestrator.RoleWAN, Port: "eth3"},
		},
		Groups: []orchestrator.GroupInput{
			{Name: "inspection-a", Ports: []orchestrator.DirectedPortInput{{Interface: "eth4", Direction: orchestrator.DirectionLANFacing}, {Interface: "eth5", Direction: orchestrator.DirectionWANFacing}}},
			{Name: "inspection-b", Ports: []orchestrator.DirectedPortInput{{Interface: "eth6", Direction: orchestrator.DirectionLANFacing}, {Interface: "eth7", Direction: orchestrator.DirectionWANFacing}}},
		},
	}
	topology, err := orchestrator.ParseTopology(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Replace(context.Background(), topology); err != nil {
		t.Fatal(err)
	}

	got := server.runtimeDataInterfaces(context.Background())
	want := []string{"eth1", "eth2", "eth3", "eth4", "eth5", "eth6", "eth7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime data interfaces = %v, want %v", got, want)
	}
}

func TestGatewayDoesNotExposeOrchestratorRepositoryRoutes(t *testing.T) {
	server := newServerForProfile(t, product.Gateway())
	response := request(t, server, http.MethodGet, orchestratorapi.TopologyPath)
	if response.Code != http.StatusNotFound {
		t.Fatalf("gateway orchestrator route status=%d body=%s", response.Code, response.Body.String())
	}
}
