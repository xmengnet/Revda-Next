//go:build linux

package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestLocalBrowserSmoke exercises the real profile/keyring path against this
// machine. It is opt-in and logs only sizes, never cookie names or values:
//
//	REVDA_BROWSER_COOKIE_SMOKE=1 go test -run TestLocalBrowserSmoke ./internal/browsercookies
func TestLocalBrowserSmoke(t *testing.T) {
	if os.Getenv("REVDA_BROWSER_COOKIE_SMOKE") == "" {
		t.Skip("set REVDA_BROWSER_COOKIE_SMOKE=1 to read this machine's browser cookies")
	}

	password, ok := linuxKeyringPassword(chromiumBrowser)
	if !ok {
		t.Log("no chromium keyring password found")
	} else {
		key := pbkdf2SHA1([]byte(password), []byte(chromeSalt), 1, 16)
		probe := encryptChromeValue(t, "v11", ".bilibili.com", "probe", key, true)
		if got, err := decryptAESCBCMulti(probe[3:], [][]byte{key}, true); err != nil || got != "probe" {
			t.Errorf("chromium keyring round-trip failed: %q, %v", got, err)
		}
		verifyRealChromeKey(t, key)
	}

	for _, browser := range []string{"chromium", "chrome", "firefox"} {
		cookie, err := FromBrowser(browser, ".bilibili.com")
		pairs := 0
		if cookie != "" {
			pairs = len(strings.Split(cookie, "; "))
		}
		t.Logf("%s: %d bytes, %d cookie pairs, err=%v", browser, len(cookie), pairs, err)
	}
}

// verifyRealChromeKey decrypts one stored v11 cookie and checks that its
// 32-byte prefix equals SHA256(host_key). Neither the host nor the value is
// logged; only the outcome of the check is.
func verifyRealChromeKey(t *testing.T, key []byte) {
	t.Helper()
	root, err := chromeRoot(chromiumBrowser)
	if err != nil {
		return
	}
	path, err := chromeCookieDB(root)
	if err != nil {
		return
	}
	snap, cleanup, err := snapshot(path)
	if err != nil {
		t.Logf("chromium snapshot: %v", err)
		return
	}
	defer cleanup()

	db, err := sql.Open("sqlite", "file:"+snap)
	if err != nil {
		t.Logf("chromium open: %v", err)
		return
	}
	defer db.Close()

	var hostKey string
	var enc []byte
	err = db.QueryRow(
		`SELECT host_key, encrypted_value FROM cookies
		 WHERE substr(encrypted_value, 1, 3) = x'763131' LIMIT 1`).Scan(&hostKey, &enc)
	if err != nil {
		t.Logf("chromium has no v11 cookie to check: %v", err)
		return
	}

	plain, err := rawChromePlaintext(enc, key)
	if err != nil {
		t.Errorf("chromium real cookie decryption failed: %v", err)
		return
	}
	sum := sha256.Sum256([]byte(hostKey))
	if len(plain) < 32 || !bytes.Equal(plain[:32], sum[:]) {
		t.Errorf("chromium key mismatch: decrypted prefix does not match the host hash")
		return
	}
	t.Logf("chromium real v11 cookie decrypts with a matching host hash")
}

func rawChromePlaintext(encrypted, key []byte) ([]byte, error) {
	if len(encrypted) < 3 || string(encrypted[:3]) != "v11" {
		return nil, errors.New("not a v11 cookie")
	}
	ct := encrypted[3:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, chromeIV).CryptBlocks(out, ct)
	return unpadPKCS7(out)
}
