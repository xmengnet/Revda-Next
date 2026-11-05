package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func encryptChromeValue(t *testing.T, version, host, value string, key []byte, hashPrefix bool) []byte {
	t.Helper()
	plain := []byte(value)
	if hashPrefix {
		sum := sha256.Sum256([]byte(host))
		plain = append(sum[:], plain...)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, chromeIV).CryptBlocks(out, plain)
	return append([]byte(version), out...)
}

func encryptGCM(t *testing.T, host, value string, key []byte, hashPrefix bool) []byte {
	t.Helper()
	plain := []byte(value)
	if hashPrefix {
		sum := sha256.Sum256([]byte(host))
		plain = append(sum[:], plain...)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x42}, gcm.NonceSize())
	sealed := gcm.Seal(nil, nonce, plain, nil)
	return append([]byte("v10"), append(append([]byte(nil), nonce...), sealed...)...)
}

func TestDecryptAESCBCMulti(t *testing.T) {
	key := pbkdf2SHA1([]byte("peanuts"), []byte(chromeSalt), 1, 16)
	const host = ".bilibili.com"

	for _, hashPrefix := range []bool{false, true} {
		enc := encryptChromeValue(t, "v10", host, "SESSDATA=abc123", key, hashPrefix)
		got, err := decryptAESCBCMulti(enc[3:], [][]byte{key}, hashPrefix)
		if err != nil {
			t.Fatalf("hashPrefix=%v: %v", hashPrefix, err)
		}
		if got != "SESSDATA=abc123" {
			t.Fatalf("hashPrefix=%v: got %q", hashPrefix, got)
		}
	}

	if _, err := decryptAESCBCMulti(encryptChromeValue(t, "v10", host, "x", key, false)[3:],
		[][]byte{make([]byte, 16)}, false); err == nil {
		t.Fatal("expected a decryption error with the wrong key")
	}
}

func TestDecryptGCM(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	enc := encryptGCM(t, ".bilibili.com", "SESSDATA=win", key, true)
	got, err := decryptGCM(enc[3:], key, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SESSDATA=win" {
		t.Fatalf("got %q", got)
	}
	if _, err := decryptGCM(enc[3:], bytes.Repeat([]byte{0x22}, 32), true); err == nil {
		t.Fatal("expected an authentication failure with the wrong key")
	}
}

func TestReadChromeCookies(t *testing.T) {
	key := pbkdf2SHA1([]byte("peanuts"), []byte(chromeSalt), 1, 16)
	dir := t.TempDir()
	path := filepath.Join(dir, "Cookies")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cookies (name TEXT, value TEXT, encrypted_value BLOB, host_key TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT, value LONGVARCHAR)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('version', '24')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookies (name, value, encrypted_value, host_key) VALUES
		('a', '1', NULL, '.bilibili.com'),
		('b', '', ?, '.bilibili.com'),
		('c', '3', NULL, '.example.com')`,
		encryptChromeValue(t, "v11", ".bilibili.com", "2", key, true)); err != nil {
		t.Fatal(err)
	}

	if got := chromeMetaVersion(db); got != 24 {
		t.Fatalf("meta version = %d", got)
	}

	decrypt := func(enc []byte) (string, error) {
		return decryptAESCBCMulti(enc[3:], [][]byte{key}, true)
	}
	got, err := readChromeCookies(db, ".bilibili.com", decrypt)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a=1; b=2" {
		t.Fatalf("got %q, want %q", got, "a=1; b=2")
	}
}

func TestFromBrowserFirefox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var profile string
	switch runtime.GOOS {
	case "darwin":
		profile = filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "abc.default")
	case "windows":
		t.Setenv("APPDATA", home)
		profile = filepath.Join(home, "Mozilla", "Firefox", "Profiles", "abc.default")
	default:
		profile = filepath.Join(home, ".mozilla", "firefox", "abc.default")
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(profile, "cookies.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE moz_cookies (name TEXT, value TEXT, host TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO moz_cookies (name, value, host) VALUES
		('SESSDATA', 'xyz', '.bilibili.com'),
		('other', 'nope', '.example.com')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := FromBrowser("firefox", ".bilibili.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "SESSDATA=xyz" {
		t.Fatalf("got %q", got)
	}

	// A configured browser replaces the manual cookie.
	got, err = Resolve("SESSDATA=manual", "firefox", ".bilibili.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "SESSDATA=xyz" {
		t.Fatalf("Resolve got %q", got)
	}
}

func TestResolveManualAndErrors(t *testing.T) {
	got, err := Resolve("SESSDATA=manual", "", ".bilibili.com")
	if err != nil || got != "SESSDATA=manual" {
		t.Fatalf("manual fallback: %q, %v", got, err)
	}
	if _, err := FromBrowser("netscape", ".bilibili.com"); err == nil {
		t.Fatal("expected an unsupported-browser error")
	}
}
