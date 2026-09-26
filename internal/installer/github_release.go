package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// DefaultDownloadBase is the GitHub releases download endpoint.
const DefaultDownloadBase = "https://github.com"

// GithubRelease installs a tool by downloading a pinned release asset and
// verifying its checksum before anything is written.
//
// The ordering here is the whole point of the package: download to a
// temporary file, verify, and only then extract. A checksum mismatch must
// leave the destination with no file at all, because an installer that
// extracts before verifying has already lost the guarantee it exists to
// provide.
type GithubRelease struct {
	// Version pins the release tag, e.g. "v1.2.3". Empty resolves "latest",
	// which is a supply-chain risk: the bytes under that URL change with no
	// record. Callers should set it from a checked-in value wherever
	// possible.
	Version string

	// DownloadBase overrides the release host. Empty means GitHub. Tests
	// point this at a local server.
	DownloadBase string

	// Dest is the directory binaries are installed into, conventionally
	// $AES_HOME/bin.
	Dest string

	// Binary overrides the filename expected inside the archive. Empty means
	// the tool name.
	Binary string

	// Client is the HTTP client used for the download. Nil means a client
	// with a sane timeout.
	Client *http.Client
}

// NewGithubRelease returns a github-release installer with defaults applied.
func NewGithubRelease() *GithubRelease { return &GithubRelease{} }

// downloadTimeout bounds a single asset download. The installer context
// carries the overall budget; this is the backstop for a server that accepts
// the connection and then stalls.
const downloadTimeout = 10 * time.Minute

// maxAssetBytes caps a downloaded asset. Release tarballs are a few megabytes;
// anything past this is a decompression bomb or a misconfigured URL.
const maxAssetBytes = 512 << 20 // 512 MiB

// Install downloads, verifies and extracts the tool's release asset.
func (g *GithubRelease) Install(ctx context.Context, a Action) error {
	if a.Host == nil {
		return fmt.Errorf("install %s: action has no host", a.Tool)
	}
	if a.Target.Repository == "" {
		return fmt.Errorf("install %s: strategy %s requires a repository", a.Tool, manifest.StrategyGithubRelease)
	}

	asset, err := assetFor(a, a.Host.Arch)
	if err != nil {
		return err
	}
	sum, err := sumFor(a, a.Host.Arch)
	if err != nil {
		return err
	}

	// The staging directory lives inside Dest so the final move is a rename
	// within one filesystem. A temp dir under /tmp would make that a copy
	// across devices, which is slower and can fail partway with a half-written
	// binary in place.
	if err := os.MkdirAll(g.Dest, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", g.Dest, err)
	}
	work, err := os.MkdirTemp(g.Dest, ".aes-download-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	// Every failure path runs through here, so a failed install never leaves
	// a partially-written file or a stale staging directory in the user's bin.
	defer os.RemoveAll(work)

	archivePath := filepath.Join(work, "asset.tar.gz")
	if err := g.download(ctx, g.assetURL(a, asset), archivePath); err != nil {
		return err
	}

	if err := verifyChecksum(archivePath, sum); err != nil {
		return err
	}

	// Extraction happens in its own directory so the verified archive is
	// never the thing sitting in Dest.
	stage := filepath.Join(work, "extract")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	binary, err := extractArchive(archivePath, stage, a.binaryName(g.Binary))
	if err != nil {
		return err
	}

	final := filepath.Join(g.Dest, a.binaryName(g.Binary))
	if err := os.Rename(binary, final); err != nil {
		return fmt.Errorf("install %s: %w", final, err)
	}
	// The tar header's mode is attacker-controlled, so the execute bit is
	// set here rather than trusted from the archive.
	if err := os.Chmod(final, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", final, err)
	}
	return nil
}

// assetFor selects the asset for one architecture.
//
// A missing key is an error naming the architecture. Falling back to amd64,
// or to whichever key happens to be first, would install a binary the machine
// cannot run — and the failure would surface much later, somewhere unrelated.
func assetFor(a Action, arch string) (string, error) {
	name, ok := a.Target.Asset[arch]
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: install %s has no asset for %s (declared: %s)",
			ErrMissingArch, a.Tool, arch, strings.Join(sortedKeys(a.Target.Asset), ", "))
	}
	return name, nil
}

// sumFor selects the recorded digest for one architecture, with the same
// no-fallback rule as assetFor.
func sumFor(a Action, arch string) (string, error) {
	sum, ok := a.Target.SHA256[arch]
	if !ok || strings.TrimSpace(sum) == "" {
		return "", fmt.Errorf("%w: install %s has no sha256 for %s (declared: %s)",
			ErrMissingArch, a.Tool, arch, strings.Join(sortedKeys(a.Target.SHA256), ", "))
	}
	return sum, nil
}

// assetURL builds the download URL for an asset.
//
// Pinned versions address the tag directly; "latest" uses the endpoint that
// redirects to it. The distinction is kept explicit so a caller pinning a
// version cannot accidentally construct the unpinned form.
func (g *GithubRelease) assetURL(a Action, asset string) string {
	base := g.DownloadBase
	if base == "" {
		base = DefaultDownloadBase
	}
	repo := strings.TrimSuffix(a.Target.Repository, "/")

	if g.Version == "" || g.Version == "latest" {
		return fmt.Sprintf("%s/%s/releases/latest/download/%s", base, repo, asset)
	}
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", base, repo, g.Version, asset)
}

// download streams a URL to a file, refusing to exceed maxAssetBytes.
func (g *GithubRelease) download(ctx context.Context, url, dest string) error {
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	// LimitReader's extra byte turns an over-large asset into a clean error
	// rather than a truncated file that would then fail checksum.
	written, err := io.Copy(out, io.LimitReader(resp.Body, maxAssetBytes+1))
	closeErr := out.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", dest, closeErr)
	}
	if written > maxAssetBytes {
		return fmt.Errorf("download %s: asset exceeds %d bytes", url, maxAssetBytes)
	}
	return nil
}

// verifyChecksum compares a file against its recorded digest.
//
// On mismatch the file is removed before the error is returned, so a corrupt
// download cannot be mistaken for a verified one by whatever runs next.
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open download: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash download: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if actual != strings.ToLower(strings.TrimSpace(expected)) {
		os.Remove(path)
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksum, expected, actual)
	}
	return nil
}

// sortedKeys returns map keys in sorted order, for deterministic error text.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
