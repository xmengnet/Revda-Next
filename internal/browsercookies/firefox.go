package browsercookies

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// firefoxRoot returns Firefox's profile directory on the current platform.
// Firefox does not follow XDG_CONFIG_HOME on Linux, so the paths differ per OS.
func firefoxRoot() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(base, "Mozilla", "Firefox", "Profiles"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles"), nil
	}
	return filepath.Join(home, ".mozilla", "firefox"), nil
}

func fromFirefox(host string) (string, error) {
	root, err := firefoxRoot()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("firefox profile not found: %w", err)
	}

	var (
		bestPath string
		bestMod  time.Time
	)
	for _, e := range entries {
		path := filepath.Join(root, e.Name(), "cookies.sqlite")
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestMod) {
			bestPath, bestMod = path, info.ModTime()
		}
	}
	if bestPath == "" {
		return "", errors.New("firefox profile with cookies.sqlite not found")
	}

	// Firefox keeps the database locked while running; read a snapshot.
	path, cleanup, err := snapshot(bestPath)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return readFirefoxCookies(path, host)
}

func readFirefoxCookies(path, host string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return "", err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT name, value FROM moz_cookies WHERE host LIKE ? ORDER BY rowid`,
		host)
	if err != nil {
		return "", fmt.Errorf("firefox cookies query: %w", err)
	}
	defer rows.Close()

	var pairs []string
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return "", err
		}
		if name == "" {
			continue
		}
		pairs = append(pairs, name+"="+value)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(pairs, "; "), nil
}
