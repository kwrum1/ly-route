package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ly-route/backend/internal/httpapi"
)

const gatewayTelemetryResponseLimit = 4 << 20

type gatewayHTTPTelemetry struct {
	endpoint string
	client   *http.Client
}

func newGatewayHTTPTelemetry(endpoint string) (*gatewayHTTPTelemetry, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, fmt.Errorf("parse gateway telemetry endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("gateway telemetry endpoint requires http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("gateway telemetry endpoint requires a host")
	}
	return &gatewayHTTPTelemetry{
		endpoint: parsed.String(),
		client:   &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func (collector *gatewayHTTPTelemetry) Collect(ctx context.Context) (httpapi.GatewayTelemetrySnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, collector.endpoint, nil)
	if err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("build gateway telemetry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := collector.client.Do(req)
	if err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("read gateway telemetry: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, gatewayTelemetryResponseLimit))
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("read gateway telemetry: status %d", res.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, gatewayTelemetryResponseLimit))
	decoder.DisallowUnknownFields()
	var snapshot httpapi.GatewayTelemetrySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("decode gateway telemetry: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, err
	}
	return snapshot, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode gateway telemetry trailer: %w", err)
	}
	return fmt.Errorf("decode gateway telemetry: multiple JSON values")
}
