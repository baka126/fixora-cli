package chatgptauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var requiredScopes = []string{"chatgpt.tokens.use.direct", "resource.invoke", "offline_access"}

type Session struct {
	ClientID    string    `json:"client_id"`
	HostID      string    `json:"ext_agent_host_id"`
	Subject     string    `json:"subject"`
	Email       string    `json:"email,omitempty"`
	Issuer      string    `json:"issuer"`
	AccessToken string    `json:"access_token"`
	Refresh     string    `json:"refresh_token"`
	TokenType   string    `json:"token_type"`
	Scopes      []string  `json:"scopes"`
	Expiry      time.Time `json:"expiry"`
	SavedAt     time.Time `json:"saved_at"`
}

func StorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fixora", "chatgpt-auth.json"), nil
}

func Load() (Session, error) {
	session, err := loadRecord()
	if err != nil {
		return Session{}, err
	}
	if session.ClientID == "" || session.AccessToken == "" || session.Refresh == "" || !hasRequiredScopes(session.Scopes) {
		return Session{}, errors.New("ChatGPT sign-in is incomplete; run `kubectl fixora auth login chatgpt` again")
	}
	return session, nil
}

func loadRecord() (Session, error) {
	path, err := StorePath()
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, fmt.Errorf("could not read ChatGPT sign-in; run `kubectl fixora auth login chatgpt` again")
	}
	return session, nil
}

func Save(session Session) error {
	path, err := StorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chatgpt-auth-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func ClearTokens() error {
	path, err := StorePath()
	if err != nil {
		return err
	}
	session, err := loadRecord()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	session.AccessToken = ""
	session.Refresh = ""
	session.TokenType = ""
	session.Scopes = nil
	session.Expiry = time.Time{}
	session.SavedAt = time.Now().UTC()
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chatgpt-auth-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func Remove() error {
	path, err := StorePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == want {
			return true
		}
	}
	return false
}

func hasRequiredScopes(scopes []string) bool {
	for _, required := range requiredScopes {
		if !hasScope(scopes, required) {
			return false
		}
	}
	return true
}
