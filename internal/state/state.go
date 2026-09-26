// Package state persists what AES believes it has installed.
//
// State is a cache, not the truth. The verifier is the ground truth; `aes
// doctor` compares the two and reports drift. Three rules follow from that, and
// the tests in this package exist to stop them eroding:
//
//  1. State is written only after the verifier returns OK. A failed install
//     never writes. The caller enforces this; nothing here can.
//  2. A corrupt state file is an error, never an empty state. Reading
//     corruption as "nothing installed" causes a reinstall storm: one truncated
//     JSON file makes aes reinstall every tool on the machine.
//  3. Writes are atomic. A crash mid-write leaves either the previous file or
//     the new one — never a half-written one.
//
// The path is a parameter rather than a constant so this package does not need
// to agree with the rest of aes about where AES_HOME lives.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version is the on-disk schema version. A file claiming a higher version was
// written by a newer aes; see ErrVersionTooNew.
const Version = 1

// State files record what is installed on a developer's machine, including
// which tools needed privilege. Nobody else's business.
const (
	DefaultFileMode os.FileMode = 0o600
	DefaultDirMode  os.FileMode = 0o700
)

var (
	// ErrCorrupt means the file exists but is not a readable state file.
	// Callers must not fall back to an empty state — see package comment.
	ErrCorrupt = errors.New("state file is corrupt")

	// ErrVersionTooNew means the file was written by a newer aes than this one.
	ErrVersionTooNew = errors.New("state file version is newer than this build supports")
)

// File is the on-disk schema. Installed is keyed by tool name.
type File struct {
	Version   int                  `json:"version"`
	Installed map[string]Installed `json:"installed"`
}

// Installed is one tool's last known good state, written only after a
// successful verify.
type Installed struct {
	Strategy   string `json:"strategy"`
	Privileged bool   `json:"requires_privilege"`
	// Version is the raw version string reported by the tool, or empty when
	// the tool has no version check. It is never fabricated.
	Version string `json:"version,omitempty"`
	// InstalledAt is RFC3339 in UTC. Callers that do not track time may leave
	// it empty; Set fills it in.
	InstalledAt string `json:"installed_at"`
}

// Store owns the in-memory state and the path it was loaded from. Every method
// is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	file File

	// writeTemp is the seam that lets tests fail a write mid-flight and assert
	// the previous file survives. Production always uses writeAll.
	writeTemp func(f *os.File, b []byte) error
}

// Open loads the state file at path.
//
// A missing file is not an error — a first run on a clean machine has nothing
// installed, and that must not look like a failure. A corrupt file is an error.
func Open(path string) (*Store, error) {
	file, err := load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, file: file, writeTemp: writeAll}, nil
}

// load reads and validates the state file at path.
func load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{Version: Version, Installed: map[string]Installed{}}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read state: %w", err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		// Rule 2. Returning a zero File here would be indistinguishable from a
		// clean machine, which is the reinstall storm this guards against.
		return File{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}

	switch {
	case f.Version == 0:
		// Every written file has a version. Its absence means the file is not
		// what it claims to be.
		return File{}, fmt.Errorf("%w: %s: missing version field", ErrCorrupt, path)
	case f.Version > Version:
		// Do not silently discard data written by a newer build. An older aes
		// that rewrote this file would drop entries the user still has.
		//
		// There is deliberately no matching guard for a *lower* version: the
		// risk here is data loss, not a stale-but-readable file. If Version ever
		// changes incompatibly, add the symmetric check then.
		return File{}, fmt.Errorf("%w: %s is version %d, this build supports %d",
			ErrVersionTooNew, path, f.Version, Version)
	}

	if f.Installed == nil {
		f.Installed = map[string]Installed{}
	}
	return f, nil
}

// Path is the file this store reads and writes.
func (s *Store) Path() string { return s.path }

// Get returns the recorded state for tool.
func (s *Store) Get(tool string) (Installed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.file.Installed[tool]
	return in, ok
}

// Set records a verified install. Callers must call this only after the
// verifier returned OK (rule 1); the timestamp is filled in when empty.
func (s *Store) Set(tool string, in Installed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file.Installed == nil {
		s.file.Installed = map[string]Installed{}
	}
	if in.InstalledAt == "" {
		in.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.file.Installed[tool] = in
}

// Remove drops a tool's entry and leaves whatever is on disk alone. This is
// `aes forget`; it is not `aes uninstall`.
func (s *Store) Remove(tool string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.file.Installed[tool]; !ok {
		return false
	}
	delete(s.file.Installed, tool)
	return true
}

// Installed returns a copy of every entry, so callers cannot mutate the store
// behind its own lock.
func (s *Store) Installed() map[string]Installed {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Installed, len(s.file.Installed))
	for name, in := range s.file.Installed {
		out[name] = in
	}
	return out
}

// Len reports how many tools are recorded.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.file.Installed)
}

// Save writes the state atomically: marshal, write to a temp file in the same
// directory, flush, rename over the target. A crash at any point leaves either
// the old file or the new one. A failed save removes its temp file.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file.Version == 0 {
		s.file.Version = Version
	}
	if s.file.Installed == nil {
		s.file.Installed = map[string]Installed{}
	}

	// encoding/json sorts map keys, so identical input yields identical bytes.
	data, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, DefaultDirMode); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Same directory as the target, so the rename stays within one filesystem
	// and is therefore atomic.
	tmp, err := os.CreateTemp(dir, ".state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()

	// Until the rename lands, the temp file is garbage. Clean it up on every
	// failure path; `committed` skips the cleanup once it is the real file.
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := s.writeTemp(tmp, data); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	// os.CreateTemp opens 0600, but the kernel still applies the process umask,
	// so set the mode outright rather than inheriting whatever umask says.
	if err := tmp.Chmod(DefaultFileMode); err != nil {
		return fmt.Errorf("chmod state: %w", err)
	}
	// Durability: the bytes must reach the disk before the rename makes them
	// visible, or a crash can leave a correctly-named but empty file.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	committed = true
	return nil
}

// writeAll is the production write path. It takes the *os.File so the test seam
// can replace the write itself rather than the commit logic around it.
func writeAll(f *os.File, b []byte) error {
	_, err := f.Write(b)
	return err
}
