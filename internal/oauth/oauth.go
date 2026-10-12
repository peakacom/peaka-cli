// Package oauth is the CLI's OAuth 2.0 public client: discovery, dynamic
// registration, authorization code with PKCE through a loopback redirect,
// refresh and revocation. It knows nothing about Peaka beyond the scopes.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const Scopes = "user_access offline_access"

// Metadata is the subset of RFC 8414 server metadata the client uses.
type Metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// TokenSet is what the CLI stores for an OAuth account (as the secret).
type TokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
}

// Expiring reports whether the access token ends within the margin.
func (t TokenSet) Expiring(margin time.Duration) bool {
	return time.Until(t.ExpiresAt) < margin
}

// Error is an OAuth error response (RFC 6749 §5.2).
type Error struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// Client talks to one authorization server.
type Client struct {
	Issuer string
	HTTP   *http.Client
	meta   *Metadata
}

func New(issuer string) *Client {
	return &Client{Issuer: strings.TrimRight(issuer, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Discover fetches the server metadata (cached per client).
func (c *Client) Discover(ctx context.Context) (*Metadata, error) {
	if c.meta != nil {
		return c.meta, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Issuer+"/.well-known/oauth-authorization-server", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authorization server %s: %w", c.Issuer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorization server %s: metadata returned HTTP %d", c.Issuer, resp.StatusCode)
	}
	var m Metadata
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("authorization server %s: bad metadata: %w", c.Issuer, err)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return nil, fmt.Errorf("authorization server %s: metadata lacks the authorization or token endpoint", c.Issuer)
	}
	c.meta = &m
	return &m, nil
}

// Register creates a public client (RFC 7591) that may redirect to the
// loopback address on any port, and returns its client_id.
func (c *Client) Register(ctx context.Context, name string) (string, error) {
	m, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	if m.RegistrationEndpoint == "" {
		return "", fmt.Errorf("authorization server %s offers no client registration; configure a client id", c.Issuer)
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                name,
		"redirect_uris":              []string{"http://127.0.0.1/callback"},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      Scopes,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.RegistrationEndpoint, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("client registration at %s: HTTP %d %s", m.RegistrationEndpoint, resp.StatusCode, firstLine(data))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.ClientID == "" {
		return "", fmt.Errorf("client registration: no client_id in the answer")
	}
	return out.ClientID, nil
}

// PKCE is a code verifier with its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

func NewPKCE() (PKCE, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return PKCE{}, err
	}
	v := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(v))
	return PKCE{Verifier: v, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// RandomState returns an unguessable state value.
func RandomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL builds the authorization request for the browser.
func (c *Client) AuthorizeURL(ctx context.Context, clientID, redirectURI, state string, p PKCE) (string, error) {
	m, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {Scopes},
		"state":                 {state},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
	}
	return m.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// Exchange redeems the authorization code.
func (c *Client) Exchange(ctx context.Context, clientID, redirectURI, code string, p PKCE) (*TokenSet, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {p.Verifier},
	})
}

// Refresh rotates the token set with the refresh token.
func (c *Client) Refresh(ctx context.Context, clientID, refreshToken string) (*TokenSet, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	})
}

func (c *Client) token(ctx context.Context, form url.Values) (*TokenSet, error) {
	m, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		e := &Error{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Code == "" {
			e.Code = fmt.Sprintf("http_%d", resp.StatusCode)
			e.Description = firstLine(data)
		}
		return nil, e
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(data, &tr); err != nil || tr.AccessToken == "" {
		return nil, errors.New("token endpoint returned no access token")
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	return &TokenSet{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second), Scope: tr.Scope}, nil
}

// Revoke asks the server to forget a token; servers that do not let public
// clients revoke answer 401, which the caller may ignore.
func (c *Client) Revoke(ctx context.Context, clientID, token string) error {
	m, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	if m.RevocationEndpoint == "" {
		return errors.New("no revocation endpoint")
	}
	form := url.Values{"token": {token}, "client_id": {clientID}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.RevocationEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("revocation: HTTP %d", resp.StatusCode)
	}
	return nil
}

// IsJWT tells a 3-part token from a Peaka API key (prefix.secret).
func IsJWT(token string) bool {
	return strings.Count(token, ".") == 2 && strings.HasPrefix(token, "eyJ")
}

// Claims decodes the payload of a JWT without verifying it (for display).
func Claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
