package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/oauth"
)

// Browser login: authorization code with PKCE through a loopback redirect
// (RFC 8252), against the authorization server of the configuration. The
// CLI registers itself once per server as a public client and keeps the
// client id next to the configurations.

const refreshMargin = 90 * time.Second

// clientID returns the public client registered for an issuer, registering one when needed.
func (r *Runtime) clientID(ctx context.Context, oc *oauth.Client) (string, error) {
	path := filepath.Join(config.Dir(), "oauth-clients.json")
	clients := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &clients)
	}
	if id := os.Getenv("PEAKA_OAUTH_CLIENT_ID"); id != "" {
		return id, nil
	}
	if id := clients[oc.Issuer]; id != "" {
		return id, nil
	}
	host, _ := os.Hostname()
	id, err := oc.Register(ctx, "Peaka CLI on "+firstNonEmpty(host, "a computer"))
	if err != nil {
		return "", api.Exit(api.ExitFailure, "%v", err)
	}
	clients[oc.Issuer] = id
	b, _ := json.MarshalIndent(clients, "", "  ")
	_ = os.MkdirAll(config.Dir(), 0o700)
	_ = os.WriteFile(path, b, 0o600)
	return id, nil
}

type browserLoginOptions struct {
	name      string
	noBrowser bool
	port      int
	timeout   time.Duration
}

// runBrowserLogin opens the browser, waits for the redirect, exchanges the
// code and stores the account.
func runBrowserLogin(o browserLoginOptions) error {
	if gf.noInput && o.noBrowser == false && !rt.StdinTTY {
		// not interactive, but the URL can still be handed to a person; keep going
	}
	issuer := rt.Settings.OAuth
	if issuer == "" {
		return api.Exit(api.ExitUsage, "no authorization server for this configuration: `peaka config set oauth URL`, or log in with --api-key")
	}
	ctx := rt.Ctx
	oc := oauth.New(issuer)
	clientID, err := rt.clientID(ctx, oc)
	if err != nil {
		return err
	}
	pkce, err := oauth.NewPKCE()
	if err != nil {
		return err
	}
	state, err := oauth.RandomState()
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.port))
	if err != nil {
		return api.Exit(api.ExitFailure, "cannot listen for the login redirect: %v", err)
	}
	defer lis.Close()
	redirectURI := fmt.Sprintf("http://%s/callback", lis.Addr().String())
	authURL, err := oc.AuthorizeURL(ctx, clientID, redirectURI, state, pkce)
	if err != nil {
		return err
	}

	result := make(chan map[string]string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/callback" {
			http.NotFound(w, req)
			return
		}
		q := req.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if q.Get("error") != "" {
			fmt.Fprint(w, callbackPage("Login cancelled", "Peaka did not authorize the CLI. You can close this window."))
		} else {
			fmt.Fprint(w, callbackPage("Logged in", "The Peaka CLI is authorized. You can close this window and return to the terminal."))
		}
		select {
		case result <- map[string]string{"code": q.Get("code"), "state": q.Get("state"), "error": q.Get("error"), "desc": q.Get("error_description")}:
		default:
		}
	})}
	go srv.Serve(lis)
	defer srv.Shutdown(context.Background())

	// The link is the request itself (client, PKCE challenge, loopback port,
	// state), so it is long; it is shown only when someone has to carry it to a
	// browser themselves.
	opened := false
	if !o.noBrowser {
		if err := openBrowser(authURL); err != nil {
			fmt.Fprintf(rt.Err, "(could not open a browser: %v)\n", err)
		} else {
			opened = true
		}
	}
	if opened {
		fmt.Fprintln(rt.Err, "Opening your browser to log in to Peaka … (--no-browser prints the link instead)")
	} else {
		fmt.Fprintf(rt.Err, "Open this link in a browser to log in to Peaka:\n  %s\n", authURL)
	}
	fmt.Fprint(rt.Err, "Waiting for the login … ")
	wait, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	var res map[string]string
	select {
	case res = <-result:
	case <-wait.Done():
		fmt.Fprintln(rt.Err)
		if ctx.Err() != nil {
			return api.Exit(api.ExitInterrupted, "interrupted")
		}
		return api.Exit(api.ExitRetryable, "no login after %s", o.timeout)
	}
	if res["error"] != "" {
		fmt.Fprintln(rt.Err, "cancelled")
		return api.Exit(api.ExitAuth, "login refused: %s%s", res["error"], suffix(res["desc"]))
	}
	// When the person had to log in first, studio's login page returns to the
	// authorization URL with "?ref=<how they logged in>" appended, which lands
	// inside the state (its last parameter) and comes back that way. The state
	// is ours as long as it starts with what we sent; what follows is theirs.
	got := res["state"]
	if got != state && !strings.HasPrefix(got, state) {
		fmt.Fprintln(rt.Err, "failed")
		if gf.debug {
			fmt.Fprintf(rt.Err, "  state sent %q, received %q\n", state, got)
		}
		return api.Exit(api.ExitAuth, "login redirect carried an unexpected state; try again (--debug shows what came back)")
	}
	tokens, err := oc.Exchange(ctx, clientID, redirectURI, res["code"], pkce)
	if err != nil {
		fmt.Fprintln(rt.Err, "failed")
		return api.Exit(api.ExitAuth, "token exchange failed: %v", err)
	}
	fmt.Fprintln(rt.Err, "✓")

	client := rt.newClient(tokens.AccessToken)
	var info map[string]any
	if err := client.Get(ctx, "/info", nil, &info); err != nil {
		return err
	}
	claims := oauth.Claims(tokens.AccessToken)
	email := firstNonEmpty(str(info["email"]), str(claims["email"]))
	acct := config.Account{
		Host:     rt.Settings.APIHost(),
		Kind:     "oauth",
		KeyType:  "USER",
		Subject:  email,
		Issuer:   issuer,
		ClientID: clientID,
		Created:  time.Now().UTC(),
	}
	acct.Name = firstNonEmpty(o.name, email, "user")
	secret, _ := json.Marshal(tokens)
	if err := config.StoreAccount(acct, string(secret)); err != nil {
		return err
	}
	rt.Config.Account = acct.Name
	if err := config.Save(rt.Config); err != nil {
		return err
	}
	rt.note("✓ Logged in as %s on %s (account %q, configuration %s)", email, acct.Host, acct.Name, rt.Config.Name)
	if rt.Settings.Format != "table" {
		return rt.print(map[string]any{"account": acct, "identity": info, "expiresAt": tokens.ExpiresAt})
	}
	return nil
}

// oauthToken returns a live access token for an OAuth account, refreshing
// (and storing the rotated refresh token) when it is about to expire.
func (r *Runtime) oauthToken(a config.Account) (string, error) {
	raw, err := config.Secret(a)
	if err != nil {
		return "", api.Exit(api.ExitAuth, "%v", err)
	}
	var t oauth.TokenSet
	if err := json.Unmarshal([]byte(raw), &t); err != nil || t.AccessToken == "" {
		return "", api.Exit(api.ExitAuth, "stored login for %q is unreadable; `peaka auth login` again", a.Name)
	}
	if !t.Expiring(refreshMargin) {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", api.Exit(api.ExitAuth, "the login for %q has expired; `peaka auth login` again", a.Name)
	}
	oc := oauth.New(a.Issuer)
	fresh, err := oc.Refresh(r.Ctx, a.ClientID, t.RefreshToken)
	if err != nil {
		var oe *oauth.Error
		if asOAuthError(err, &oe) && (oe.Code == "invalid_grant" || oe.Status == 400 || oe.Status == 401) {
			return "", api.Exit(api.ExitAuth, "the login for %q is no longer valid (%v); `peaka auth login` again", a.Name, err)
		}
		return "", &api.Problem{Code: "UPSTREAM_UNAVAILABLE", Detail: fmt.Sprintf("token refresh: %v", err), Retryable: true}
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = t.RefreshToken
	}
	secret, _ := json.Marshal(fresh)
	if err := config.StoreAccount(a, string(secret)); err != nil {
		return "", err
	}
	return fresh.AccessToken, nil
}

func asOAuthError(err error, target **oauth.Error) bool {
	e, ok := err.(*oauth.Error)
	if ok {
		*target = e
	}
	return ok
}

// revokeAccount tells the authorization server to forget an OAuth account's tokens (best effort).
func (r *Runtime) revokeAccount(a config.Account) {
	if a.Kind != "oauth" || a.Issuer == "" {
		return
	}
	raw, err := config.Secret(a)
	if err != nil {
		return
	}
	var t oauth.TokenSet
	if json.Unmarshal([]byte(raw), &t) != nil {
		return
	}
	oc := oauth.New(a.Issuer)
	ctx, cancel := context.WithTimeout(r.Ctx, 10*time.Second)
	defer cancel()
	if t.RefreshToken != "" {
		_ = oc.Revoke(ctx, a.ClientID, t.RefreshToken)
	}
	_ = oc.Revoke(ctx, a.ClientID, t.AccessToken)
}
