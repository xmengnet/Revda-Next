//go:build !linux && !darwin && !windows

package browsercookies

import "errors"

func chromeRoot(chromeKind) (string, error) {
	return "", errors.New("browser cookie import is not implemented on this platform")
}

func chromeDecryptor(chromeKind, string, int) (func([]byte) (string, error), error) {
	return nil, errors.New("browser cookie import is not implemented on this platform")
}
