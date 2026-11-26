package chatgptauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	issuerURL = "https://auth.openai.com"
	resource  = "https://api.openai.com/v1"
	clientNew = "dynamic_agent_client"
	appName   = "Fixora CLI"
)

type OpenBrowser func(string) error

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

type callbackResult struct {
	code, state, clientID, oauthError string
}

func Login(ctx context.Context, openBrowser OpenBrowser) (Session, error) {
	discoveryDoc, err := discover(ctx)
	if err != nil {
		return Session{}, err
	}
	hostID, err := loadOrCreateHostID()
	if err != nil {
		return Session{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Session{}, fmt.Errorf("could not start local sign-in callback: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)
	state, err := randomString(32)
	if err != nil {
		return Session{}, err
	}
	nonce, err := randomString(32)
	if err != nil {
		return Session{}, err
	}
	verifier, err := randomString(48)
	if err != nil {
		return Session{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	clientID := clientNew
	values := url.Values{
		"client_id": {clientID}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {redirectURI},
		"scope":    {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"},
		"resource": {resource}, "state": {state}, "nonce": {nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	savedSession, _ := loadRecord()
	if savedSession.ClientID != "" {
		saved := savedSession
		clientID = saved.ClientID
		values.Set("client_id", clientID)
		if saved.Email != "" {
			values.Set("login_hint", saved.Email)
		}
	} else {
		values.Set("agent_name_hint", appName)
	}
	authorizeURL := discoveryDoc.AuthorizationEndpoint + "?" + values.Encode()
	resultCh := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Sign-in callback method not allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case resultCh <- callbackResult{code: r.URL.Query().Get("code"), state: r.URL.Query().Get("state"), clientID: r.URL.Query().Get("client_id"), oauthError: r.URL.Query().Get("error")}:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<!doctype html><title>Fixora sign-in</title><p>Sign-in received. Return to the Fixora terminal.</p>")
		default:
			http.Error(w, "Sign-in callback already received", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Shutdown(context.Background())
	if openBrowser == nil {
		openBrowser = openSystemBrowser
	}
	if err := openBrowser(authorizeURL); err != nil {
		return Session{}, fmt.Errorf("could not open your browser; open this sign-in link manually:\n%s", authorizeURL)
	}
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	var callback callbackResult
	select {
	case <-ctx.Done():
		return Session{}, ctx.Err()
	case <-timer.C:
		return Session{}, errors.New("ChatGPT sign-in timed out; run `kubectl fixora auth login chatgpt` to try again")
	case callback = <-resultCh:
	}
	if callback.state != state {
		return Session{}, errors.New("ChatGPT sign-in could not be verified; state did not match")
	}
	if callback.oauthError != "" {
		if callback.oauthError == "access_denied" {
			return Session{}, errors.New("ChatGPT sign-in was cancelled; approve Fixora’s requested access to continue")
		}
		return Session{}, fmt.Errorf("ChatGPT sign-in failed (%s)", safeOAuthError(callback.oauthError))
	}
	issuedClientID := clientID
	if clientID == clientNew {
		issuedClientID = strings.TrimSpace(callback.clientID)
	} else if callback.clientID != "" && callback.clientID != clientID {
		return Session{}, errors.New("ChatGPT sign-in returned a different account registration; please try again")
	}
	if callback.code == "" || issuedClientID == "" || issuedClientID == clientNew {
		return Session{}, errors.New("ChatGPT did not complete Fixora registration; please try sign-in again")
	}
	tokens, err := exchangeCode(ctx, discoveryDoc.TokenEndpoint, issuedClientID, callback.code, verifier, redirectURI)
	if err != nil {
		return Session{}, err
	}
	if tokens.IDToken == "" || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return Session{}, errors.New("ChatGPT sign-in returned an incomplete session")
	}
	claims, err := validateIDToken(ctx, discoveryDoc, tokens.IDToken, issuedClientID, nonce)
	if err != nil {
		return Session{}, fmt.Errorf("ChatGPT sign-in could not be verified: %w", err)
	}
	scopes := splitScopes(tokens.Scope)
	if !hasRequiredScopes(scopes) {
		return Session{}, errors.New("ChatGPT sign-in did not grant plan usage; run sign-in again and approve Fixora’s ChatGPT plan access")
	}
	if savedSession.ClientID == issuedClientID && savedSession.Subject != "" && savedSession.Subject != claims.Subject {
		return Session{}, errors.New("this ChatGPT registration is linked to a different account; sign in with the original account. Account switching is not supported yet")
	}
	if hostID == "" {
		return Session{}, errors.New("could not persist Fixora sign-in identity")
	}
	session := Session{
		ClientID: issuedClientID, HostID: hostID, Subject: claims.Subject, Email: claims.Email, Issuer: discoveryDoc.Issuer,
		AccessToken: tokens.AccessToken, Refresh: tokens.RefreshToken,
		TokenType: firstNonEmpty(tokens.TokenType, "Bearer"), Scopes: scopes,
		Expiry: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC(), SavedAt: time.Now().UTC(),
	}
	if tokens.ExpiresIn <= 0 {
		return Session{}, errors.New("ChatGPT sign-in returned an invalid token lifetime")
	}
	if err := Save(session); err != nil {
		return Session{}, fmt.Errorf("could not securely save ChatGPT sign-in: %w", err)
	}
	return session, nil
}

func Logout(ctx context.Context) (bool, error) {
	session, err := loadRecord()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err := Remove(); err != nil {
			return false, err
		}
		return false, errors.New("local ChatGPT sign-in was cleared, but remote revocation could not be confirmed; disconnect Fixora in ChatGPT settings if needed")
	}
	if session.Refresh == "" || session.ClientID == "" {
		return false, ClearTokens()
	}
	doc, err := discover(ctx)
	if err != nil {
		_ = ClearTokens()
		return false, err
	}
	form := url.Values{"token": {session.Refresh}, "token_type_hint": {"refresh_token"}, "client_id": {session.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		_ = ClearTokens()
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, callErr := client.Do(req)
	revoked := callErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300
	if resp != nil {
		resp.Body.Close()
	}
	removeErr := ClearTokens()
	if removeErr != nil {
		return revoked, removeErr
	}
	if callErr != nil {
		return false, fmt.Errorf("local sign-in cleared, but ChatGPT revocation could not be confirmed; disconnect Fixora in ChatGPT settings to revoke it")
	}
	return revoked, nil
}

func discover(ctx context.Context) (discovery, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, issuerURL+"/.well-known/openid-configuration", nil)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return discovery{}, errors.New("could not reach ChatGPT sign-in service")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discovery{}, fmt.Errorf("ChatGPT sign-in service returned HTTP %d", resp.StatusCode)
	}
	var doc discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return discovery{}, errors.New("ChatGPT sign-in service returned invalid configuration")
	}
	if doc.Issuer != issuerURL || !trustedAuthEndpoint(doc.AuthorizationEndpoint) || !trustedAuthEndpoint(doc.TokenEndpoint) || !trustedAuthEndpoint(doc.JWKSURI) || !trustedAuthEndpoint(doc.RevocationEndpoint) {
		return discovery{}, errors.New("ChatGPT sign-in service configuration was not trusted")
	}
	return doc, nil
}

func trustedAuthEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "auth.openai.com"
}

func exchangeCode(ctx context.Context, endpoint, clientID, code, verifier, redirectURI string) (tokenResponse, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI}, "resource": {resource}}
	return postToken(ctx, endpoint, form)
}

func splitScopes(value string) []string {
	return strings.Fields(value)
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func loadOrCreateHostID() (string, error) {
	if session, err := loadRecord(); err == nil && session.HostID != "" {
		return session.HostID, nil
	}
	hostID := "urn:uuid:" + uuid.NewString()
	if err := Save(Session{HostID: hostID, SavedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	return hostID, nil
}

func openSystemBrowser(raw string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{raw}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", raw}
	default:
		command, args = "xdg-open", []string{raw}
	}
	return exec.Command(command, args...).Start()
}

func safeOAuthError(value string) string {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "unknown"
		}
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
