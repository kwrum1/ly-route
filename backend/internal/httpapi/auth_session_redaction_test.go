package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAuthenticationResponsesNeverExposeBearerSessionID(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for name, response := range map[string]string{
		"login":   login.Body.String(),
		"session": authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/auth/session", "", cookie).Body.String(),
	} {
		var body map[string]any
		if err := json.Unmarshal([]byte(response), &body); err != nil {
			t.Fatal(err)
		}
		session, ok := body["session"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no public session: %s", name, response)
		}
		if _, exists := session["id"]; exists || strings.Contains(response, cookie.Value) {
			t.Fatalf("%s leaked session bearer credential: %s", name, response)
		}
	}
}
