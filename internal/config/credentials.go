package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// Account is one stored identity. The secret lives in the OS keychain when
// there is one, else in credentials.json (0600); accounts.json only indexes them.
type Account struct {
	Name     string    `json:"name"`
	Host     string    `json:"host"` // API host the credential belongs to
	Kind     string    `json:"kind"` // api-key | oauth
	KeyType  string    `json:"keyType,omitempty"`
	KeyID    string    `json:"keyId,omitempty"`
	Subject  string    `json:"subject,omitempty"` // what /info said: org or project name, email
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed,omitempty"`
}

func (a Account) key() string { return a.Host + "/" + a.Name }

const keyringService = "peaka-cli"

func accountsPath() string    { return filepath.Join(Dir(), "accounts.json") }
func credentialsPath() string { return filepath.Join(Dir(), "credentials.json") }

type accountsFile struct {
	Accounts []Account `json:"accounts"`
}

func readAccounts() (*accountsFile, error) {
	f := &accountsFile{}
	b, err := os.ReadFile(accountsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil
		}
		return nil, err
	}
	return f, json.Unmarshal(b, f)
}

func writeAccounts(f *accountsFile) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	sort.Slice(f.Accounts, func(i, j int) bool { return f.Accounts[i].key() < f.Accounts[j].key() })
	b, _ := json.MarshalIndent(f, "", "  ")
	return os.WriteFile(accountsPath(), b, 0o600)
}

// Accounts lists stored identities, optionally for one host.
func Accounts(host string) ([]Account, error) {
	f, err := readAccounts()
	if err != nil {
		return nil, err
	}
	var out []Account
	for _, a := range f.Accounts {
		if host == "" || a.Host == host {
			out = append(out, a)
		}
	}
	return out, nil
}

// FindAccount returns the account named name on host.
func FindAccount(host, name string) (*Account, error) {
	f, err := readAccounts()
	if err != nil {
		return nil, err
	}
	for i := range f.Accounts {
		if f.Accounts[i].Host == host && f.Accounts[i].Name == name {
			return &f.Accounts[i], nil
		}
	}
	return nil, nil
}

// StoreAccount saves the index entry and the secret.
func StoreAccount(a Account, secret string) error {
	f, err := readAccounts()
	if err != nil {
		return err
	}
	replaced := false
	for i := range f.Accounts {
		if f.Accounts[i].key() == a.key() {
			f.Accounts[i] = a
			replaced = true
		}
	}
	if !replaced {
		f.Accounts = append(f.Accounts, a)
	}
	if err := storeSecret(a.key(), secret); err != nil {
		return err
	}
	return writeAccounts(f)
}

// RemoveAccount deletes the index entry and the secret.
func RemoveAccount(host, name string) error {
	f, err := readAccounts()
	if err != nil {
		return err
	}
	kept := f.Accounts[:0]
	found := false
	for _, a := range f.Accounts {
		if a.Host == host && a.Name == name {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("account %q is not stored for %s", name, host)
	}
	f.Accounts = kept
	_ = deleteSecret(host + "/" + name)
	return writeAccounts(f)
}

// Secret returns an account's secret.
func Secret(a Account) (string, error) {
	return loadSecret(a.key())
}

// --- secret storage: keychain first, credentials.json as the fallback ---

func useFileStore() bool { return os.Getenv("PEAKA_CREDENTIALS_STORE") == "file" }

func storeSecret(key, secret string) error {
	if !useFileStore() {
		if err := keyring.Set(keyringService, key, secret); err == nil {
			_ = fileSecretDelete(key)
			return nil
		}
	}
	return fileSecretSet(key, secret)
}

func loadSecret(key string) (string, error) {
	if !useFileStore() {
		if s, err := keyring.Get(keyringService, key); err == nil && s != "" {
			return s, nil
		}
	}
	s, err := fileSecretGet(key)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("no stored secret for %s (peaka auth login)", key)
	}
	return s, nil
}

func deleteSecret(key string) error {
	_ = keyring.Delete(keyringService, key)
	return fileSecretDelete(key)
}

type credentialsFile map[string]string

func readCredentials() (credentialsFile, error) {
	c := credentialsFile{}
	b, err := os.ReadFile(credentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	return c, json.Unmarshal(b, &c)
}

func writeCredentials(c credentialsFile) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(credentialsPath(), b, 0o600)
}

func fileSecretSet(key, secret string) error {
	c, err := readCredentials()
	if err != nil {
		return err
	}
	c[key] = secret
	return writeCredentials(c)
}

func fileSecretGet(key string) (string, error) {
	c, err := readCredentials()
	if err != nil {
		return "", err
	}
	return c[key], nil
}

func fileSecretDelete(key string) error {
	c, err := readCredentials()
	if err != nil {
		return err
	}
	if _, ok := c[key]; !ok {
		return nil
	}
	delete(c, key)
	return writeCredentials(c)
}

// MaskSecret shows a credential's shape without its value.
func MaskSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}
