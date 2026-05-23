package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"golden_eye/internal/config"
)

func TestManager_Defaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")

	m, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	s := m.Get()
	if s.RetentionDays != 30 {
		t.Errorf("default retention_days: got %d, want 30", s.RetentionDays)
	}
	if s.SegmentMinutes != 10 {
		t.Errorf("default segment_minutes: got %d, want 10", s.SegmentMinutes)
	}
}

func TestManager_UpdateAndPersist(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")

	m, _ := config.NewManager(path)

	newSettings := config.Settings{RetentionDays: 7, SegmentMinutes: 5}
	if err := m.Update(newSettings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// In-memory value updated.
	got := m.Get()
	if got.RetentionDays != 7 {
		t.Errorf("retention_days after update: got %d, want 7", got.RetentionDays)
	}

	// File persisted — load fresh manager from same path.
	m2, err := config.NewManager(path)
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	got2 := m2.Get()
	if got2.RetentionDays != 7 {
		t.Errorf("retention_days after reload: got %d, want 7", got2.RetentionDays)
	}
}

func TestManager_AtomicWrite(t *testing.T) {
	// Verify no temp file is left behind after a successful write.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.json")

	m, _ := config.NewManager(path)
	if err := m.Update(config.Settings{RetentionDays: 14, SegmentMinutes: 10}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestManager_Validation(t *testing.T) {
	tmp := t.TempDir()
	m, _ := config.NewManager(filepath.Join(tmp, "config.json"))

	tests := []struct {
		name    string
		s       config.Settings
		wantErr bool
	}{
		{"zero retention", config.Settings{RetentionDays: 0, SegmentMinutes: 10}, true},
		{"zero segment", config.Settings{RetentionDays: 30, SegmentMinutes: 0}, true},
		{"valid", config.Settings{RetentionDays: 1, SegmentMinutes: 1}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := m.Update(tc.s)
			if (err != nil) != tc.wantErr {
				t.Errorf("Update(%+v) err=%v, wantErr=%v", tc.s, err, tc.wantErr)
			}
		})
	}
}
