package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"ly-route/backend/internal/runtime/dnsguard"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path := strings.TrimSpace(os.Getenv("LY_ROUTE_DNS_GUARD_CONFIG"))
	if path == "" {
		path = "/etc/ly-route/dns-guard.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read DNS guard config: %w", err)
	}
	var config dnsguard.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse DNS guard config: %w", err)
	}
	guard, err := dnsguard.New(config)
	if err != nil {
		return fmt.Errorf("initialize DNS guard: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return guard.Serve(ctx)
}
