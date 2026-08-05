package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// discoveredEndpoints holds the OAuth endpoints resolved from server metadata.
type discoveredEndpoints struct {
	AuthURL  string
	TokenURL string
	Scopes   []string
}

// protectedResourceMetadata is the subset of RFC 9728 metadata we consume.
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMetadata is the subset of RFC 8414 / OpenID discovery we consume.
type authServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// discoverOAuthEndpoints resolves a resource server's OAuth endpoints by chaining
// RFC 9728 protected-resource metadata (to find the authorization server) with
// RFC 8414 authorization-server metadata (to find the authorize/token URLs).
func discoverOAuthEndpoints(ctx context.Context, httpc *http.Client, resourceURL string) (discoveredEndpoints, error) {
	prm, err := fetchProtectedResourceMetadata(ctx, httpc, resourceURL)
	if err != nil {
		return discoveredEndpoints{}, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return discoveredEndpoints{}, fmt.Errorf("protected-resource metadata lists no authorization servers")
	}

	asm, err := fetchAuthServerMetadata(ctx, httpc, prm.AuthorizationServers[0])
	if err != nil {
		return discoveredEndpoints{}, err
	}
	if asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return discoveredEndpoints{}, fmt.Errorf("authorization-server metadata missing authorization_endpoint or token_endpoint")
	}

	scopes := prm.ScopesSupported
	if len(scopes) == 0 {
		scopes = asm.ScopesSupported
	}
	return discoveredEndpoints{
		AuthURL:  asm.AuthorizationEndpoint,
		TokenURL: asm.TokenEndpoint,
		Scopes:   scopes,
	}, nil
}

// fetchProtectedResourceMetadata retrieves RFC 9728 metadata for resourceURL. It
// tries the path-aware well-known location first, then the origin-root location.
func fetchProtectedResourceMetadata(ctx context.Context, httpc *http.Client, resourceURL string) (protectedResourceMetadata, error) {
	u, err := url.Parse(resourceURL)
	if err != nil {
		return protectedResourceMetadata{}, fmt.Errorf("invalid server url: %w", err)
	}
	var lastErr error
	for _, wk := range wellKnownURLs(u, "oauth-protected-resource") {
		var prm protectedResourceMetadata
		if err := getJSON(ctx, httpc, wk, &prm); err != nil {
			lastErr = err
			continue
		}
		if len(prm.AuthorizationServers) > 0 {
			return prm, nil
		}
		lastErr = fmt.Errorf("no authorization_servers at %s", wk)
	}
	return protectedResourceMetadata{}, fmt.Errorf("protected-resource metadata: %w", lastErr)
}

// fetchAuthServerMetadata retrieves RFC 8414 metadata for an issuer, trying the
// oauth-authorization-server well-known first, then openid-configuration.
func fetchAuthServerMetadata(ctx context.Context, httpc *http.Client, issuer string) (authServerMetadata, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return authServerMetadata{}, fmt.Errorf("invalid issuer url: %w", err)
	}
	var lastErr error
	candidates := append(wellKnownURLs(u, "oauth-authorization-server"), wellKnownURLs(u, "openid-configuration")...)
	for _, wk := range candidates {
		var asm authServerMetadata
		if err := getJSON(ctx, httpc, wk, &asm); err != nil {
			lastErr = err
			continue
		}
		if asm.AuthorizationEndpoint != "" && asm.TokenEndpoint != "" {
			return asm, nil
		}
		lastErr = fmt.Errorf("incomplete metadata at %s", wk)
	}
	return authServerMetadata{}, fmt.Errorf("authorization-server metadata: %w", lastErr)
}

// wellKnownURLs returns candidate well-known metadata URLs for u and the given
// suffix, per RFC 8414/9728: the well-known segment is inserted between the host
// and any path component, and (when a path exists) also appended after it.
func wellKnownURLs(u *url.URL, suffix string) []string {
	origin := u.Scheme + "://" + u.Host
	path := strings.Trim(u.Path, "/")
	out := []string{origin + "/.well-known/" + suffix}
	if path != "" {
		out = append([]string{origin + "/.well-known/" + suffix + "/" + path}, out...)
	}
	return out
}

// getJSON performs a GET and decodes a JSON body into v, capping the read size.
func getJSON(ctx context.Context, httpc *http.Client, rawURL string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return nil
}
