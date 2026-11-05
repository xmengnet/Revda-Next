//go:build darwin

package browsercookies

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func chromeRoot(k chromeKind) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := "Google/Chrome"
	if k == chromiumBrowser {
		dir = "Chromium"
	}
	return filepath.Join(home, "Library", "Application Support", dir), nil
}

// chromeDecryptor derives the macOS key from the Keychain password. Chrome's
// macOS derivation uses 1003 PBKDF2 iterations, unlike Linux's single one.
func chromeDecryptor(k chromeKind, _ string, metaVersion int) (func([]byte) (string, error), error) {
	var key []byte
	if password, ok := macKeychainPassword(k.keyLabel); ok {
		key = pbkdf2SHA1([]byte(password), []byte(chromeSalt), 1003, 16)
	}
	hashPrefix := metaVersion >= 24

	return func(encrypted []byte) (string, error) {
		if len(encrypted) < 3 {
			return "", errors.New("cookie value is empty")
		}
		if string(encrypted[:3]) == "v10" {
			if key == nil {
				return "", errors.New("cannot decrypt v10 cookies: no Safe Storage key in the Keychain")
			}
			return decryptAESCBCMulti(encrypted[3:], [][]byte{key}, hashPrefix)
		}
		// Anything else is old Chrome data stored without encryption.
		return string(encrypted), nil
	}, nil
}

func macKeychainPassword(label string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security",
		"find-generic-password",
		"-w",
		"-a", label,
		"-s", label+" Safe Storage",
	).Output()
	if err != nil {
		return "", false
	}
	pw := strings.TrimSpace(string(out))
	if pw == "" {
		return "", false
	}
	return pw, true
}
