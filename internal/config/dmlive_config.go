package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// DmliveConfig mirrors ~/.config/dmlive/config.toml, which is shared with the
// Rust dmlive binary. danmaku_speed is in MILLISECONDS there (Rust default
// 8000, valid range 1000-30000) — never write seconds into it.
type DmliveConfig struct {
	BCookie            string  `toml:"bcookie,omitempty"`
	CookiesFromBrowser string  `toml:"cookies_from_browser,omitempty"`
	DanmakuSpeed       uint64  `toml:"danmaku_speed,omitempty"`
	FontAlpha          float64 `toml:"font_alpha,omitempty"`
	FontScale          float64 `toml:"font_scale,omitempty"`
	DanmakuArea        int     `toml:"danmaku_area,omitempty"`   // percent of the 1080p canvas the scroll lanes may use
	DanmakuAnchor      string  `toml:"danmaku_anchor,omitempty"` // top | middle | bottom
	RecordPath         string  `toml:"record_path,omitempty"`
}

type DmliveConfigManager struct {
	mu         sync.RWMutex
	configPath string
	data       DmliveConfig
}

func GetDmliveConfigPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}

	var dmliveDir string
	if runtime.GOOS == "windows" {
		dmliveDir = filepath.Join(configDir, "THMonster", "dmlive")
	} else {
		dmliveDir = filepath.Join(configDir, "dmlive")
	}
	return filepath.Join(dmliveDir, "config.toml")
}

func NewDmliveConfigManager() (*DmliveConfigManager, error) {
	path := GetDmliveConfigPath()
	m := &DmliveConfigManager{
		configPath: path,
		data: DmliveConfig{
			DanmakuSpeed:  8000,
			FontAlpha:     0.0,
			FontScale:     1.0,
			DanmakuArea:   50,
			DanmakuAnchor: "top",
		},
	}
	_ = m.Load()
	return m, nil
}

func (m *DmliveConfigManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var c DmliveConfig
	if err := toml.Unmarshal(data, &c); err != nil {
		return err
	}
	// Sanitize danmaku_speed to Rust dmlive's millisecond convention. Values
	// below 1000 can only be seconds written by an older Go build.
	switch {
	case c.DanmakuSpeed == 0:
		c.DanmakuSpeed = 8000
	case c.DanmakuSpeed < 1000:
		c.DanmakuSpeed *= 1000
	case c.DanmakuSpeed > 30000:
		c.DanmakuSpeed = 30000
	}
	if c.FontScale <= 0 {
		c.FontScale = 1.0
	}
	switch {
	case c.DanmakuArea <= 0:
		c.DanmakuArea = 50 // the engine's historical top half
	case c.DanmakuArea < 10:
		c.DanmakuArea = 10
	case c.DanmakuArea > 100:
		c.DanmakuArea = 100
	}
	switch strings.ToLower(strings.TrimSpace(c.DanmakuAnchor)) {
	case "top", "middle", "center", "centre", "bottom":
		c.DanmakuAnchor = strings.ToLower(strings.TrimSpace(c.DanmakuAnchor))
	default:
		c.DanmakuAnchor = "top"
	}
	m.data = c
	return nil
}

func (m *DmliveConfigManager) Save() error {
	m.mu.RLock()
	c := m.data
	m.mu.RUnlock()

	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	bytes, err := toml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(m.configPath, bytes, 0644)
}

func (m *DmliveConfigManager) Get() DmliveConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data
}

func (m *DmliveConfigManager) Update(c DmliveConfig) error {
	m.mu.Lock()
	m.data = c
	m.mu.Unlock()
	return m.Save()
}
