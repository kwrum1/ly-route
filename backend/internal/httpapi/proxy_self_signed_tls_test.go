package httpapi

import (
	"context"
	"net/http"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestSubscriptionRefreshAlwaysAcceptsSelfSignedTLS(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-self-signed-subscription?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	acceptSelfSigned := false
	server := New(
		WithStore(store),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(fixedClock()),
		WithSubscriptionFetcher(func(_ context.Context, _ string, insecureTLS bool) ([]byte, error) {
			acceptSelfSigned = insecureTLS
			return []byte("trojan://secret@node.example:443?security=tls&sni=node.example#Private"), nil
		}),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions", `{"id":"private","name":"Private","enabled":true,"selection":"fixed","url":"https://provider.example/sub"}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create subscription status=%d body=%s", created.Code, created.Body.String())
	}
	refreshed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions/private/refresh", `{}`, cookie)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh subscription status=%d body=%s", refreshed.Code, refreshed.Body.String())
	}
	if !acceptSelfSigned {
		t.Fatal("subscription refresh did not enable self-signed TLS compatibility")
	}
}
