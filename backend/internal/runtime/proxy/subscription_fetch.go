package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func FetchSubscription(ctx context.Context, rawURL string, _ bool) ([]byte, error) {
	endpoint, err := validateSubscriptionEndpoint(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   subscriptionTLSConfig(),
		DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			addresses, resolveErr := net.DefaultResolver.LookupNetIP(dialCtx, "ip", host)
			if resolveErr != nil {
				return nil, resolveErr
			}
			for _, address := range addresses {
				if !allowedSubscriptionAddress(address) {
					return nil, fmt.Errorf("%w: endpoint resolves to a non-public address", ErrInvalidSubscription)
				}
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("%w: endpoint has no addresses", ErrInvalidSubscription)
			}
			return dialer.DialContext(dialCtx, network, net.JoinHostPort(addresses[0].String(), port))
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("%w: too many subscription redirects", ErrInvalidSubscription)
			}
			_, redirectErr := validateSubscriptionEndpoint(request.Context(), request.URL.String())
			return redirectErr
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create request: %v", ErrInvalidSubscription, err)
	}
	request.Header.Set("User-Agent", "LY-Route/1 subscription-refresh")
	request.Header.Set("Accept", "text/plain, application/octet-stream;q=0.9")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: subscription fetch failed; verify endpoint reachability and TLS settings", ErrInvalidSubscription)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: subscription returned HTTP %d", ErrInvalidSubscription, response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, MaxSubscriptionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read subscription: %v", ErrInvalidSubscription, err)
	}
	if len(content) == 0 || len(content) > MaxSubscriptionBytes {
		return nil, fmt.Errorf("%w: subscription response size is invalid", ErrInvalidSubscription)
	}
	return content, nil
}

func subscriptionTLSConfig() *tls.Config {
	// Router subscriptions commonly use private PKI. Keep TLS encryption while
	// accepting certificates outside the system trust store.
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec
}

func validateSubscriptionEndpoint(ctx context.Context, rawURL string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil {
		return nil, fmt.Errorf("%w: subscription endpoint must be an HTTPS URL without userinfo", ErrInvalidSubscription)
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", endpoint.Hostname())
	if err != nil {
		return nil, fmt.Errorf("%w: resolve subscription endpoint: %v", ErrInvalidSubscription, err)
	}
	for _, address := range addresses {
		if !allowedSubscriptionAddress(address) {
			return nil, fmt.Errorf("%w: endpoint resolves to a non-public address", ErrInvalidSubscription)
		}
	}
	return endpoint, nil
}

func allowedSubscriptionAddress(address netip.Addr) bool {
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast()
}
