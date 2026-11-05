package browsercookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// chromeIV is what Chrome uses on Linux and macOS: sixteen spaces.
var chromeIV = []byte("                ")

const chromeSalt = "saltysalt"

// pbkdf2SHA1 implements PBKDF2-HMAC-SHA1, as Chrome's os_crypt uses with a
// different iteration count per platform.
func pbkdf2SHA1(password, salt []byte, iterations, keyLen int) []byte {
	out := make([]byte, 0, keyLen)
	for block := uint32(1); len(out) < keyLen; block++ {
		mac := hmac.New(sha1.New, password)
		mac.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], block)
		mac.Write(idx[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha1.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// decryptAESCBCMulti tries each candidate key and returns the first plaintext
// that decrypts to valid UTF-8. Chrome from schema 24 on prefixes a SHA-256 of
// the cookie's host key, which is stripped when hashPrefix is set.
func decryptAESCBCMulti(ciphertext []byte, keys [][]byte, hashPrefix bool) (string, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("bad ciphertext length")
	}
	for _, key := range keys {
		block, err := aes.NewCipher(key)
		if err != nil {
			continue
		}
		plain := make([]byte, len(ciphertext))
		cipher.NewCBCDecrypter(block, chromeIV).CryptBlocks(plain, ciphertext)
		plain, err = unpadPKCS7(plain)
		if err != nil {
			continue
		}
		plain = stripHashPrefix(plain, hashPrefix)
		if utf8.Valid(plain) {
			return string(plain), nil
		}
	}
	return "", errors.New("decryption failed (wrong key or unsupported format)")
}

// decryptGCM decrypts Chrome's Windows v10 format: 12-byte nonce, ciphertext,
// 16-byte authentication tag (AES-GCM).
func decryptGCM(ciphertext, key []byte, hashPrefix bool) (string, error) {
	const nonceLen, tagLen = 12, 16
	if len(ciphertext) < nonceLen+tagLen {
		return "", errors.New("bad ciphertext length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := ciphertext[:nonceLen]
	body := ciphertext[nonceLen : len(ciphertext)-tagLen]
	tag := ciphertext[len(ciphertext)-tagLen:]
	sealed := append(append([]byte(nil), body...), tag...)
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", errors.New("decryption failed (GCM authentication)")
	}
	plain = stripHashPrefix(plain, hashPrefix)
	if !utf8.Valid(plain) {
		return "", errors.New("decrypted cookie is not valid UTF-8")
	}
	return string(plain), nil
}

func stripHashPrefix(plain []byte, hashPrefix bool) []byte {
	if !hashPrefix || len(plain) <= 32 {
		if hashPrefix {
			return nil
		}
		return plain
	}
	return plain[32:]
}

func unpadPKCS7(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("invalid padding")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("invalid padding")
	}
	if !bytes.Equal(b[len(b)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errors.New("invalid padding")
	}
	return b[:len(b)-n], nil
}

func joinCookiePairs(pairs []string) string {
	var out bytes.Buffer
	for i, pair := range pairs {
		if i > 0 {
			out.WriteString("; ")
		}
		out.WriteString(pair)
	}
	return out.String()
}
