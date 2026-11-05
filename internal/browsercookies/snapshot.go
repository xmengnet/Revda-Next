package browsercookies

import (
	"io"
	"os"
	"path/filepath"
)

// snapshot copies a browser SQLite database and its WAL/journal sidecars into
// a private temporary directory. Browsers keep these files open and write to
// them, so reading a copy avoids lock errors and never mutates the profile.
func snapshot(src string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "revda-browser-cookies-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	dst := filepath.Join(dir, filepath.Base(src))
	if err := copyFile(src, dst); err != nil {
		cleanup()
		return "", nil, err
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if _, err := os.Stat(src + suffix); err == nil {
			// A missing sidecar only means there was no uncheckpointed data.
			_ = copyFile(src+suffix, dst+suffix)
		}
	}
	return dst, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
