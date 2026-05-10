package vm

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

// EnvironmentVariables is a small in-process container for environment variables.
// It does not try to provide secrecy guarantees for process memory.
type EnvironmentVariables struct {
	mu   sync.RWMutex
	vars map[string]string
}

// NewEnvironmentVariables returns an empty environment store.
func NewEnvironmentVariables() *EnvironmentVariables {
	return &EnvironmentVariables{
		vars: make(map[string]string),
	}
}

// FromFile reads a file (.env style format) to load environment variables.
func FromFile(path string) (*EnvironmentVariables, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	vars := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: invalid line: %s", ErrEnvInvalidFile, line)
		}
		vars[parts[0]] = parts[1]
	}
	return &EnvironmentVariables{vars: vars}, nil
}

// Set stores value under name.
func (e *EnvironmentVariables) Set(name, value string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vars[name] = value
	return nil
}

// get returns the stored value.
func (e *EnvironmentVariables) get(name string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	value, ok := e.vars[name]
	return value, ok
}

// Delete removes a variable.
func (e *EnvironmentVariables) Delete(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.vars, name)
}

// Len returns the number of stored variables.
func (e *EnvironmentVariables) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.vars)
}

// Keys returns sorted environment variable names.
func (e *EnvironmentVariables) Keys() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.vars))
	for name := range e.vars {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Destroy clears the map contents and releases references.
func (e *EnvironmentVariables) Destroy() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.vars)
	e.vars = nil
}

// SaveToFile writes the variables in the style of a .env file
func (e *EnvironmentVariables) SaveToFile(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	for key, value := range e.vars {
		if _, err := file.WriteString(fmt.Sprintf("%s=%s\n", key, value)); err != nil {
			return err
		}
	}
	return nil
}
