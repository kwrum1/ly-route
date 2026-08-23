package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultXrayMetricsAddress = "127.0.0.1:11111"

const xrayMetricsResponseLimit = 4 << 20

type XrayObservatoryState struct {
	OutboundTag       string
	Alive             bool
	DelayMilliseconds int64
	LastTryTime       time.Time
}

type XrayObservatoryStateController interface {
	XrayObservatoryStates(context.Context) ([]XrayObservatoryState, error)
}

func (controller FilesystemController) XrayObservatoryStates(ctx context.Context) ([]XrayObservatoryState, error) {
	address, err := loopbackXrayMetricsAddress(controller.XrayMetricsAddress)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/debug/vars", nil)
	if err != nil {
		return nil, fmt.Errorf("build Xray observatory request: %w", err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("read Xray observatory: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, xrayMetricsResponseLimit))
		return nil, fmt.Errorf("read Xray observatory: status %d", response.StatusCode)
	}
	var payload struct {
		Observatory map[string]struct {
			Alive       bool   `json:"alive"`
			Delay       int64  `json:"delay"`
			OutboundTag string `json:"outbound_tag"`
			LastTryTime int64  `json:"last_try_time"`
		} `json:"observatory"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, xrayMetricsResponseLimit))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Xray observatory: %w", err)
	}
	states := make([]XrayObservatoryState, 0, len(payload.Observatory))
	seen := make(map[string]bool, len(payload.Observatory))
	for _, observation := range payload.Observatory {
		tag := strings.TrimSpace(observation.OutboundTag)
		if tag == "" || !serviceTokenSafe(tag) || seen[tag] || observation.Delay < 0 {
			return nil, fmt.Errorf("Xray observatory returned an invalid outbound state")
		}
		seen[tag] = true
		states = append(states, XrayObservatoryState{
			OutboundTag: tag, Alive: observation.Alive, DelayMilliseconds: observation.Delay,
			LastTryTime: time.Unix(observation.LastTryTime, 0).UTC(),
		})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].OutboundTag < states[j].OutboundTag })
	return states, nil
}

func loopbackXrayMetricsAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		address = DefaultXrayMetricsAddress
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("Xray metrics address is invalid: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("Xray metrics must use a numeric loopback address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("Xray metrics port is invalid")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}
