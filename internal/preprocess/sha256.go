package preprocess

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// HashReader returns the hex-encoded SHA256 of all bytes read from r.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// HashFile returns the hex-encoded SHA256 of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return HashReader(f)
}
