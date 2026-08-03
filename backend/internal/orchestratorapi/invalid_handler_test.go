package orchestratorapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestHandler_invalid_topology_matrix_preserves_checksum_with_zero_writes(t *testing.T) {
	// Given
	fixture, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var valid testTopologyDTO
	if err := json.Unmarshal(fixture, &valid); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	for _, test := range invalidTopologyCases() {
		t.Run(test.name, func(t *testing.T) {
			// Given
			repository, server := newTestHTTPServer(t)
			seed := sendRequest(t, server, http.MethodPut, TopologyPath, fixture)
			if seed.StatusCode != http.StatusOK {
				t.Fatalf("seed topology = %d %s", seed.StatusCode, seed.Body)
			}
			before, err := repository.Checksum(context.Background())
			if err != nil {
				t.Fatalf("Checksum before invalid write: %v", err)
			}
			invalid := test.mutate(cloneTestTopology(valid))
			body, err := json.Marshal(invalid)
			if err != nil {
				t.Fatalf("encode invalid topology: %v", err)
			}

			// When
			response := sendRequest(t, server, http.MethodPut, TopologyPath, body)

			// Then
			if response.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("PUT invalid topology = %d %s, want 422", response.StatusCode, response.Body)
			}
			var apiError errorResponse
			decodeResponse(t, response.Body, &apiError)
			if apiError.Error.Code != codeForError(test.wantErr) {
				t.Fatalf("error code = %q, want error class %v", apiError.Error.Code, test.wantErr)
			}
			after, err := repository.Checksum(context.Background())
			if err != nil {
				t.Fatalf("Checksum after invalid write: %v", err)
			}
			if after != before {
				t.Fatalf("checksum after invalid write = %q, want unchanged %q", after, before)
			}
		})
	}
}
