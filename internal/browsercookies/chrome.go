package browsercookies

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

// chromeKind distinguishes the Chromium-based profiles supported here.
type chromeKind struct {
	dirName   string // profile directory under the platform's config root
	keyLabel  string // "<label> Safe Storage" in the OS key store
	secretApp string // libsecret application attribute (Linux)
}

var (
	chromeBrowser   = chromeKind{"google-chrome", "Chrome", "chrome"}
	chromiumBrowser = chromeKind{"chromium", "Chromium", "chromium"}
)

func fromChrome(k chromeKind, host string) (string, error) {
	root, err := chromeRoot(k)
	if err != nil {
		return "", err
	}
	cookiePath, err := chromeCookieDB(root)
	if err != nil {
		return "", fmt.Errorf("%s Cookies file not found: %w", k.keyLabel, err)
	}

	// Chrome keeps the file open while running; read a private snapshot.
	path, cleanup, err := snapshot(cookiePath)
	if err != nil {
		return "", err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return "", err
	}
	defer db.Close()

	// Chrome records a schema version in "meta"; from 24 on, encrypted
	// values begin with a SHA-256 of the cookie's host key.
	metaVersion := chromeMetaVersion(db)
	decrypt, err := chromeDecryptor(k, root, metaVersion)
	if err != nil {
		return "", err
	}
	return readChromeCookies(db, host, decrypt)
}

// chromeCookieDB finds the cookie database. Chrome 96+ moved it to
// Default/Network/Cookies, older profiles still carry Default/Cookies.
func chromeCookieDB(root string) (string, error) {
	candidates := []string{
		filepath.Join(root, "Default", "Network", "Cookies"),
		filepath.Join(root, "Default", "Cookies"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Cookies database under %s", root)
}

func chromeMetaVersion(db *sql.DB) int {
	var raw string
	if err := db.QueryRow(`SELECT CAST(value AS TEXT) FROM meta WHERE key = 'version'`).Scan(&raw); err != nil {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return v
}

// readChromeCookies decodes every cookie for host. Unencrypted rows (value
// column) are used as-is; encrypted_value goes through the platform decryptor,
// whose error aborts the read, as in Rust dmlive.
func readChromeCookies(db *sql.DB, host string, decrypt func([]byte) (string, error)) (string, error) {
	rows, err := db.Query(
		`SELECT name, COALESCE(value, ''), encrypted_value FROM cookies WHERE host_key LIKE ? ORDER BY rowid`,
		host)
	if err != nil {
		return "", fmt.Errorf("chrome cookies query: %w", err)
	}
	defer rows.Close()

	var pairs []string
	for rows.Next() {
		var name, value string
		var encrypted []byte
		if err := rows.Scan(&name, &value, &encrypted); err != nil {
			return "", err
		}
		if name == "" {
			continue
		}
		if value == "" {
			if len(encrypted) == 0 {
				continue
			}
			plain, err := decrypt(encrypted)
			if err != nil {
				return "", fmt.Errorf("decrypt cookie %q: %w", name, err)
			}
			value = plain
		}
		pairs = append(pairs, name+"="+value)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return joinCookiePairs(pairs), nil
}
