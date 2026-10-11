package logx

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redacted replaces every secret the scrubber removes.
const Redacted = "<redacted>"

// patterns match bearer JWTs and ephemeral client secrets whatever their source.
var patterns = regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+|ek_[\w-]+`)

// registry holds every secret the process has seen, so opaque formats that no
// pattern matches are removed too. It only grows: a secret seen once stays secret.
var registry struct {
	mu      sync.RWMutex
	secrets map[string]struct{}
	ordered []string // longest first, so a secret containing another is removed whole
}

// RegisterSecret adds s to the process-wide scrub list. Empty strings are ignored.
func RegisterSecret(s string) {
	if s == "" {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.secrets == nil {
		registry.secrets = map[string]struct{}{}
	}
	if _, ok := registry.secrets[s]; ok {
		return
	}
	registry.secrets[s] = struct{}{}
	registry.ordered = append(registry.ordered, s)
	sort.SliceStable(registry.ordered, func(i, j int) bool {
		return len(registry.ordered[i]) > len(registry.ordered[j])
	})
}

// Scrub removes every registered secret and every secret-shaped pattern from s.
// Use it on any text that may reach the user or an artifact, such as error messages.
func Scrub(s string) string {
	registry.mu.RLock()
	for _, sec := range registry.ordered {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, Redacted)
		}
	}
	registry.mu.RUnlock()
	return patterns.ReplaceAllString(s, Redacted)
}
