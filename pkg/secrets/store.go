package secrets

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Entry is a secret with metadata.
type Entry struct {
	Name      string
	Value     string
	Scope     string // e.g. "org/repo" or "pipeline:deploy"
	CreatedBy string
	UpdatedAt time.Time
}

// Store is an in-memory secret store. In production this would be
// backed by Vault or SOPS. This provides the interface and log-scrubbing
// logic that any backend must support.
type Store struct {
	mu      sync.RWMutex
	secrets map[string]map[string]*Entry // scope -> name -> entry
}

// NewStore creates an empty secret store.
func NewStore() *Store {
	return &Store{
		secrets: make(map[string]map[string]*Entry),
	}
}

// Put creates or updates a secret.
func (s *Store) Put(scope, name, value, createdBy string) error {
	if scope == "" || name == "" {
		return fmt.Errorf("scope and name are required")
	}
	if value == "" {
		return fmt.Errorf("secret value cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.secrets[scope] == nil {
		s.secrets[scope] = make(map[string]*Entry)
	}
	s.secrets[scope][name] = &Entry{
		Name:      name,
		Value:     value,
		Scope:     scope,
		CreatedBy: createdBy,
		UpdatedAt: time.Now(),
	}
	return nil
}

// Get retrieves a secret by scope and name.
func (s *Store) Get(scope, name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scopeSecrets, ok := s.secrets[scope]
	if !ok {
		return "", fmt.Errorf("no secrets for scope %q", scope)
	}
	entry, ok := scopeSecrets[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found in scope %q", name, scope)
	}
	return entry.Value, nil
}

// GetMultiple retrieves multiple secrets for a scope.
// Returns a map of name -> value. Missing secrets return an error.
func (s *Store) GetMultiple(scope string, names []string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string, len(names))
	scopeSecrets := s.secrets[scope]

	for _, name := range names {
		if scopeSecrets == nil {
			return nil, fmt.Errorf("secret %q not found in scope %q", name, scope)
		}
		entry, ok := scopeSecrets[name]
		if !ok {
			return nil, fmt.Errorf("secret %q not found in scope %q", name, scope)
		}
		result[name] = entry.Value
	}
	return result, nil
}

// Delete removes a secret.
func (s *Store) Delete(scope, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	scopeSecrets, ok := s.secrets[scope]
	if !ok {
		return fmt.Errorf("scope %q not found", scope)
	}
	if _, ok := scopeSecrets[name]; !ok {
		return fmt.Errorf("secret %q not found in scope %q", name, scope)
	}
	delete(scopeSecrets, name)
	if len(scopeSecrets) == 0 {
		delete(s.secrets, scope)
	}
	return nil
}

// List returns metadata (not values) for all secrets in a scope.
func (s *Store) List(scope string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entries []Entry
	for _, entry := range s.secrets[scope] {
		entries = append(entries, Entry{
			Name:      entry.Name,
			Scope:     entry.Scope,
			CreatedBy: entry.CreatedBy,
			UpdatedAt: entry.UpdatedAt,
			// Value intentionally omitted.
		})
	}
	return entries
}

// Scrubber replaces secret values in log output with "***".
type Scrubber struct {
	mu       sync.RWMutex
	patterns []*regexp.Regexp
	values   []string
}

// NewScrubber creates a log scrubber for the given secret values.
func NewScrubber(secretValues []string) *Scrubber {
	s := &Scrubber{}
	for _, v := range secretValues {
		if len(v) < 3 {
			continue // don't scrub very short values, too many false positives
		}
		s.values = append(s.values, v)
		s.patterns = append(s.patterns, regexp.MustCompile(regexp.QuoteMeta(v)))
	}
	return s
}

// Scrub replaces all known secret values in the input with "***".
func (s *Scrubber) Scrub(input string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := input
	for i, pattern := range s.patterns {
		_ = s.values[i] // keep in sync
		result = pattern.ReplaceAllString(result, "***")
	}
	return result
}

// AddValue adds a new secret value to scrub.
func (s *Scrubber) AddValue(value string) {
	if len(value) < 3 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, value)
	s.patterns = append(s.patterns, regexp.MustCompile(regexp.QuoteMeta(value)))
}

// ScrubEnv takes a set of env vars and returns a scrubbed version
// suitable for logging. Keys containing common secret patterns are masked.
func ScrubEnv(env map[string]string) map[string]string {
	sensitivePatterns := []string{
		"SECRET", "TOKEN", "PASSWORD", "KEY", "CREDENTIAL",
		"AUTH", "PRIVATE", "API_KEY",
	}

	result := make(map[string]string, len(env))
	for k, v := range env {
		upper := strings.ToUpper(k)
		masked := false
		for _, pattern := range sensitivePatterns {
			if strings.Contains(upper, pattern) {
				result[k] = "***"
				masked = true
				break
			}
		}
		if !masked {
			result[k] = v
		}
	}
	return result
}
