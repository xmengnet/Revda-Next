//go:build windows

package browsercookies

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func chromeRoot(k chromeKind) (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	dir := `Google\Chrome\User Data`
	if k == chromiumBrowser {
		dir = `Chromium\User Data`
	}
	return filepath.Join(base, dir), nil
}

// chromeDecryptor follows Chrome's Windows os_crypt: the AES-GCM key for v10
// lives in Local State, protected by DPAPI; values without a known prefix are
// DPAPI blobs themselves. Chrome 127+ app-bound encryption (v20) cannot be
// decrypted from outside the browser's elevation service.
func chromeDecryptor(k chromeKind, root string, metaVersion int) (func([]byte) (string, error), error) {
	key, keyErr := windowsV10Key(root)
	hashPrefix := metaVersion >= 24

	return func(encrypted []byte) (string, error) {
		if len(encrypted) < 3 {
			return "", errors.New("cookie value is empty")
		}
		switch string(encrypted[:3]) {
		case "v10":
			if keyErr != nil {
				return "", fmt.Errorf("windows cookie key: %w", keyErr)
			}
			return decryptGCM(encrypted[3:], key, hashPrefix)
		case "v20":
			return "", errors.New("Chrome app-bound encryption (v20) is not supported; use Firefox or paste the cookie manually")
		default:
			plain, err := decryptDPAPI(encrypted)
			if err != nil {
				return "", err
			}
			return string(plain), nil
		}
	}, nil
}

type localState struct {
	OSCrypt struct {
		EncryptedKey string `json:"encrypted_key"`
	} `json:"os_crypt"`
}

func windowsV10Key(root string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, "Local State"))
	if err != nil {
		return nil, err
	}
	var state localState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.OSCrypt.EncryptedKey == "" {
		return nil, errors.New("no os_crypt.encrypted_key in Local State")
	}
	raw, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(raw, []byte("DPAPI")) {
		return nil, errors.New("unexpected os_crypt key prefix")
	}
	return decryptDPAPI(raw[len("DPAPI"):])
}

type dataBlob struct {
	size uint32
	data *byte
}

var (
	crypt32           = syscall.NewLazyDLL("crypt32.dll")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree     = kernel32.NewProc("LocalFree")
)

func decryptDPAPI(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	in := dataBlob{size: uint32(len(blob)), data: &blob[0]}
	var out dataBlob
	ok, _, callErr := procUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		0,
		uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %v", callErr)
	}
	if out.data == nil || out.size == 0 {
		return nil, errors.New("CryptUnprotectData returned no data")
	}
	result := make([]byte, out.size)
	copy(result, unsafe.Slice(out.data, out.size))
	procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return result, nil
}
