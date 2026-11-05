//go:build linux

package browsercookies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func chromeRoot(k chromeKind) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, k.dirName), nil
}

// chromeDecryptor derives Chrome's Linux keys. v10 used the fixed "peanuts"
// password, v11 the keyring's "Safe Storage" password; trying both (plus an
// empty password, as Chromium itself does) covers profiles written by either
// backend.
func chromeDecryptor(k chromeKind, _ string, metaVersion int) (func([]byte) (string, error), error) {
	peanuts := pbkdf2SHA1([]byte("peanuts"), []byte(chromeSalt), 1, 16)
	empty := pbkdf2SHA1(nil, []byte(chromeSalt), 1, 16)
	v10Keys := [][]byte{peanuts, empty}
	v11Keys := [][]byte{peanuts, empty}
	if password, ok := linuxKeyringPassword(k); ok {
		keyring := pbkdf2SHA1([]byte(password), []byte(chromeSalt), 1, 16)
		v10Keys = append(v10Keys, keyring)
		v11Keys = [][]byte{keyring, peanuts, empty}
	}

	hashPrefix := metaVersion >= 24
	return func(encrypted []byte) (string, error) {
		if len(encrypted) < 3 {
			return "", errors.New("cookie value is empty")
		}
		var keys [][]byte
		switch string(encrypted[:3]) {
		case "v10":
			keys = v10Keys
		case "v11":
			keys = v11Keys
		default:
			return "", fmt.Errorf("unsupported cookie encryption %q", string(encrypted[:3]))
		}
		return decryptAESCBCMulti(encrypted[3:], keys, hashPrefix)
	}, nil
}

// linuxKeyringPassword asks KWallet first (what Rust dmlive does), then
// libsecret, which is what GNOME and bare compositors use.
func linuxKeyringPassword(k chromeKind) (string, bool) {
	if pw, ok := kwalletPassword(k.keyLabel); ok {
		return pw, true
	}
	return secretToolPassword(k.secretApp)
}

func kwalletPassword(label string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dbus-send",
		"--session", "--print-reply=literal",
		"--dest=org.kde.kwalletd5", "/modules/kwalletd5",
		"org.kde.KWallet.networkWallet").Output()
	if err != nil {
		return "", false
	}
	wallet := strings.TrimSpace(string(out))
	if wallet == "" {
		return "", false
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	out, err = exec.CommandContext(ctx2, "kwallet-query",
		"--read-password", label+" Safe Storage",
		"--folder", label+" Keys",
		wallet).Output()
	if err != nil {
		return "", false
	}
	pw := strings.TrimSpace(string(out))
	if pw == "" || strings.HasPrefix(pw, "Failed") {
		return "", false
	}
	return pw, true
}

func secretToolPassword(app string) (string, bool) {
	queries := [][]string{
		{"lookup", "application", app},
		{"lookup", "xdg:schema", "chrome_libsecret_os_crypt_password_v2", "application", app},
		{"lookup", "xdg:schema", "chrome_libsecret_os_crypt_password", "application", app},
	}
	for _, args := range queries {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, "secret-tool", args...).Output()
		cancel()
		if err != nil {
			continue
		}
		if pw := strings.TrimSpace(string(out)); pw != "" {
			return pw, true
		}
	}
	return "", false
}
