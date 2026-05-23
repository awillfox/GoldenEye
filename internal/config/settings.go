package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const defaultSettingsPath = "config.json"

// Settings holds runtime values editable from the dashboard.
// Written atomically to config.json; all reads go through RWMutex.
type Settings struct {
	RetentionDays  int `json:"retention_days"`
	SegmentMinutes int `json:"segment_minutes"`
}

// Manager loads, stores, and atomically persists Settings.
type Manager struct {
	mu   sync.RWMutex
	path string
	s    Settings
}

// NewManager loads settings from path (default config.json).
// Missing file is fine — defaults are applied.
func NewManager(path string) (*Manager, error) {
	if path == "" {
		path = defaultSettingsPath
	}
	m := &Manager{
		path: path,
		s: Settings{
			RetentionDays:  30,
			SegmentMinutes: 10,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read settings %q: %w", path, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m.s); err != nil {
			return nil, fmt.Errorf("parse settings %q: %w", path, err)
		}
	}

	return m, nil
}

// Get returns a snapshot of the current settings.
func (m *Manager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.s
}

// Update replaces the current settings and persists them atomically.
func (m *Manager) Update(s Settings) error {
	if s.RetentionDays < 1 {
		return fmt.Errorf("retention_days must be >= 1")
	}
	if s.SegmentMinutes < 1 {
		return fmt.Errorf("segment_minutes must be >= 1")
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	// Write to a temp file in the same directory, then rename for atomicity.
	tmp, err := os.CreateTemp(".", ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp settings file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Clean up temp file if rename failed.
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp settings: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp settings: %w", err)
	}
	if err = os.Rename(tmpName, m.path); err != nil {
		return fmt.Errorf("rename settings file: %w", err)
	}

	m.mu.Lock()
	m.s = s
	m.mu.Unlock()
	return nil
}
