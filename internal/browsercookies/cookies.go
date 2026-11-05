// Package browsercookies reads login cookies from a local browser profile,
// ported from Rust dmlive's utils/cookies.rs. Only the requested host is
// read; callers pass ".bilibili.com".
package browsercookies

import (
	"fmt"
	"strings"
)

// FromBrowser returns a Cookie header value built from the cookies the named
// browser has for host. Supported browsers: chrome, chromium, firefox.
func FromBrowser(browser, host string) (string, error) {
	switch b := strings.ToLower(strings.TrimSpace(browser)); b {
	case "chrome":
		return fromChrome(chromeBrowser, host)
	case "chromium":
		return fromChrome(chromiumBrowser, host)
	case "firefox":
		return fromFirefox(host)
	default:
		return "", fmt.Errorf("browser not supported: %q (use chrome, chromium or firefox)", browser)
	}
}

// Resolve picks the cookie to send, mirroring Rust dmlive: when
// cookies_from_browser is set it replaces the manually configured cookie, and
// a browser read failure is reported instead of silently falling back.
func Resolve(manualCookie, browser, host string) (string, error) {
	browser = strings.ToLower(strings.TrimSpace(browser))
	if browser == "" {
		return manualCookie, nil
	}
	cookie, err := FromBrowser(browser, host)
	if err != nil {
		return "", fmt.Errorf("cookies_from_browser=%s: %w", browser, err)
	}
	return cookie, nil
}
