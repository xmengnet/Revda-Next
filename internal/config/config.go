package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Theme   string   `toml:"theme,omitempty"`
	Saved   []string `toml:"saved"`
	History []string `toml:"history"`
}

type Manager struct {
	mu         sync.RWMutex
	configPath string
	data       Config
}

func GetConfigPath() string {
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
	return filepath.Join(dmliveDir, "revda.toml")
}

func NewManager() (*Manager, error) {
	path := GetConfigPath()
	m := &Manager{
		configPath: path,
		data: Config{
			Saved:   make([]string, 0),
			History: make([]string, 0),
		},
	}
	if err := m.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		// If file doesn't exist, we start with empty config, else log or return error
	}
	return m, nil
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return err
	}

	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return err
	}
	if c.Saved == nil {
		c.Saved = make([]string, 0)
	}
	if c.History == nil {
		c.History = make([]string, 0)
	}
	m.data = c
	return nil
}

func (m *Manager) Save() error {
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

func (m *Manager) GetTheme() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.data.Theme == "" {
		return "system"
	}
	return m.data.Theme
}

func (m *Manager) SetTheme(theme string) error {
	m.mu.Lock()
	m.data.Theme = theme
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) GetSaved() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]string, len(m.data.Saved))
	copy(res, m.data.Saved)
	return res
}

func (m *Manager) AddSaved(roomCode string) error {
	m.mu.Lock()
	for _, code := range m.data.Saved {
		if code == roomCode {
			m.mu.Unlock()
			return nil
		}
	}
	m.data.Saved = append(m.data.Saved, roomCode)
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) RemoveSaved(roomCode string) error {
	m.mu.Lock()
	newSaved := make([]string, 0, len(m.data.Saved))
	for _, code := range m.data.Saved {
		if code != roomCode {
			newSaved = append(newSaved, code)
		}
	}
	m.data.Saved = newSaved
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) GetHistory() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]string, len(m.data.History))
	copy(res, m.data.History)
	return res
}

func (m *Manager) AddHistory(roomCode string) error {
	m.mu.Lock()
	newHistory := make([]string, 0, len(m.data.History)+1)
	for _, code := range m.data.History {
		if code != roomCode {
			newHistory = append(newHistory, code)
		}
	}
	newHistory = append(newHistory, roomCode)
	// Keep up to 36 items as in original Revda
	if len(newHistory) > 36 {
		newHistory = newHistory[len(newHistory)-36:]
	}
	m.data.History = newHistory
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) RemoveHistory(roomCode string) error {
	m.mu.Lock()
	newHistory := make([]string, 0, len(m.data.History))
	for _, code := range m.data.History {
		if code != roomCode {
			newHistory = append(newHistory, code)
		}
	}
	m.data.History = newHistory
	m.mu.Unlock()
	return m.Save()
}
