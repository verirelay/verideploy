package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/verirelay/verideploy/internal/atomic"
	"github.com/verirelay/verideploy/internal/network"
)

const maxBootFileSize = 512 << 20

// Fetch makes dir hold release tag of repository, checked by verify. A dir
// that already verifies is kept as it is, so the files a running CVM boots are
// never replaced. Otherwise the release is downloaded into a staging
// directory beside dir and moves into place only once it verifies. A missing
// build-records.json is left for verify to report.
func Fetch(ctx context.Context, c *http.Client, repository, tag, dir string, verify func(string) error, log io.Writer) error {
	if verify(dir) == nil {
		_, _ = fmt.Fprintf(log, "  %s is already present and verified\n", tag)
		return nil
	}
	// Tags start with a letter or digit, so no tag names these directories.
	staging := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".fetch")
	old := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".old")
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	// Release files are public; plan and status read them without root.
	if err := os.MkdirAll(staging, 0o755); err != nil { //nolint:gosec // G301: public release files.
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	base := repository + "/releases/download/" + url.PathEscape(tag) + "/"
	for _, name := range MetadataFiles {
		err := download(ctx, c, base+name, filepath.Join(staging, name), maxMetadataSize, "")
		if name == "build-records.json" && network.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("download %s: %w", name, err)
		}
	}
	sums, err := readSums(filepath.Join(staging, "SHA256SUMS"))
	if err != nil {
		return err
	}
	for _, name := range BootFiles {
		want, ok := sums[name]
		if !ok {
			return fmt.Errorf("SHA256SUMS does not list %s", name)
		}
		_, _ = fmt.Fprintf(log, "  downloading %s\n", name)
		if err := download(ctx, c, base+name, filepath.Join(staging, name), maxBootFileSize, want); err != nil {
			return fmt.Errorf("download %s: %w", name, err)
		}
	}
	if err := verify(staging); err != nil {
		return err
	}
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

// download replaces path with the body at address. With a non-empty sha256,
// a body with another digest leaves path untouched.
func download(ctx context.Context, c *http.Client, address, path string, limit int64, sha256Hex string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return atomic.Write(path, 0o644, func(w io.Writer) error {
		h := sha256.New()
		if err := network.Copy(ctx, c, address, io.MultiWriter(w, h), limit); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); sha256Hex != "" && got != sha256Hex {
			return fmt.Errorf("sha256 is %s, SHA256SUMS says %s", got, sha256Hex)
		}
		return nil
	})
}
