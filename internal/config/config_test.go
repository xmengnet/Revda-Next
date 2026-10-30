package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadAndSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "revda-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "revda.toml")
	m := &Manager{
		configPath: cfgPath,
		data: Config{
			Saved:   []string{"bi-123"},
			History: []string{"do-456"},
		},
	}

	if err := m.Save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	m2 := &Manager{configPath: cfgPath}
	if err := m2.Load(); err != nil {
		t.Fatalf("load error: %v", err)
	}

	if len(m2.GetSaved()) != 1 || m2.GetSaved()[0] != "bi-123" {
		t.Errorf("unexpected saved: %v", m2.GetSaved())
	}
	if len(m2.GetHistory()) != 1 || m2.GetHistory()[0] != "do-456" {
		t.Errorf("unexpected history: %v", m2.GetHistory())
	}

	_ = m2.AddSaved("hu-789")
	if len(m2.GetSaved()) != 2 {
		t.Errorf("expected 2 saved rooms")
	}

	_ = m2.RemoveSaved("bi-123")
	if len(m2.GetSaved()) != 1 || m2.GetSaved()[0] != "hu-789" {
		t.Errorf("unexpected saved after remove: %v", m2.GetSaved())
	}
}
