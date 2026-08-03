package gateway

import "testing"

func TestGatewayAuthDefaultsRequireFactoryPasswordChange(t *testing.T) {
	for _, key := range []string{
		"LY_ROUTE_ADMIN_USERNAME",
		"LY_ROUTE_ADMIN_PASSWORD",
		"LY_ROUTE_FORCE_PASSWORD_CHANGE",
	} {
		t.Setenv(key, "")
	}

	config := gatewayAuthConfig()
	if config.AdminUsername != "admin" {
		t.Fatalf("default admin username = %q, want admin", config.AdminUsername)
	}
	if config.AdminPassword != "password" {
		t.Fatalf("default admin password = %q, want password", config.AdminPassword)
	}
	if !config.ForcePasswordChange {
		t.Fatal("factory admin account must require a password change")
	}
}
