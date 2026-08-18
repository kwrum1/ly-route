package httpapi

import (
	"net/http"
	"testing"

	"ly-route/backend/internal/product"
)

func TestNormalizeObjectGroupRejectsCrossKindIDReuse(t *testing.T) {
	server, _, cookie := productTestServer(t, product.Gateway())
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"same-id","kind":"ip","name":"IP 组","entries":["192.0.2.1"]}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create IP group status=%d body=%s", created.Code, created.Body.String())
	}
	conflict := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"same-id","kind":"domain","name":"域名组","entries":["example.com"]}`, cookie)
	if conflict.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-kind ID reuse status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}
