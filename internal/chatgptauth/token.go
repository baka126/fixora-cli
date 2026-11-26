package chatgptauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

var refreshMu sync.Mutex

type TokenSource struct {
	ctx context.Context
}

func NewTokenSource(ctx context.Context) oauth2.TokenSource {
	return &TokenSource{ctx: ctx}
}

func (s *TokenSource) Token() (*oauth2.Token, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	unlock, err := acquireRefreshLock(s.ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	session, err := Load()
	if err != nil {
		return nil, err
	}
	if time.Until(session.Expiry) > 60*time.Second {
		return &oauth2.Token{AccessToken: session.AccessToken, RefreshToken: session.Refresh, TokenType: session.TokenType, Expiry: session.Expiry}, nil
	}
	if session.Refresh == "" {
		return nil, errors.New("ChatGPT sign-in expired; run `kubectl fixora auth login chatgpt` again")
	}
	doc, err := discover(s.ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {session.ClientID}, "refresh_token": {session.Refresh}, "resource": {resource}}
	refreshed, err := postToken(s.ctx, doc.TokenEndpoint, form)
	if err != nil {
		var oauthErr *tokenEndpointError
		if errors.As(err, &oauthErr) && isTerminalRefreshError(oauthErr.Code) {
			_ = ClearTokens()
			return nil, errors.New("ChatGPT sign-in expired; run `kubectl fixora auth login chatgpt` again")
		}
		return nil, err
	}
	if refreshed.AccessToken == "" || refreshed.ExpiresIn <= 0 {
		return nil, errors.New("ChatGPT could not refresh its sign-in; run `kubectl fixora auth login chatgpt` again")
	}
	if refreshed.Scope != "" {
		refreshedScopes := splitScopes(refreshed.Scope)
		if !hasRequiredScopes(refreshedScopes) {
			return nil, errors.New("ChatGPT sign-in no longer allows plan usage; run `kubectl fixora auth login chatgpt` again")
		}
		session.Scopes = refreshedScopes
	}
	session.AccessToken = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		session.Refresh = refreshed.RefreshToken
	}
	session.TokenType = firstNonEmpty(refreshed.TokenType, session.TokenType, "Bearer")
	session.Expiry = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second).UTC()
	session.SavedAt = time.Now().UTC()
	if err := Save(session); err != nil {
		return nil, fmt.Errorf("could not securely save refreshed ChatGPT sign-in: %w", err)
	}
	return &oauth2.Token{AccessToken: session.AccessToken, RefreshToken: session.Refresh, TokenType: session.TokenType, Expiry: session.Expiry}, nil
}

func isTerminalRefreshError(code string) bool {
	switch code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
		return true
	default:
		return false
	}
}

func acquireRefreshLock(ctx context.Context) (func(), error) {
	path, err := StorePath()
	if err != nil {
		return nil, err
	}
	lockDir := path + ".refresh-lock"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		err := os.Mkdir(lockDir, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lockDir) }, nil
		}
		if !os.IsExist(err) {
			return nil, errors.New("could not lock ChatGPT token refresh")
		}
		if info, statErr := os.Stat(lockDir); statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.Remove(lockDir)
			continue
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("another Fixora process is refreshing the ChatGPT session; retry in a moment")
}

type tokenEndpointError struct{ Code string }

func (e *tokenEndpointError) Error() string { return "ChatGPT token request failed" }

func postToken(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, errors.New("could not start ChatGPT token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, errors.New("could not reach ChatGPT token service")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, errors.New("could not read ChatGPT token response")
	}
	var result tokenResponse
	if json.Unmarshal(body, &result) != nil {
		return tokenResponse{}, fmt.Errorf("ChatGPT token service returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, &tokenEndpointError{Code: safeOAuthError(result.Error)}
	}
	return result, nil
}
