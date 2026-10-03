package authkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// TokenStore persists an OAuth2 token between CLI / desktop process runs.
// Implementations must be safe for read-modify-write loops (Save followed by
// Load returns the saved value) and must surface a nil token with a nil
// error when no token is currently stored.
type TokenStore interface {
	// Save persists t. Implementations that write to disk should restrict
	// permissions to the owning user.
	Save(t *oauth2.Token) error

	// Load returns the currently stored token, or (nil, nil) if none.
	// Returns a non-nil error only when the storage backend is reachable
	// but unreadable (e.g. corrupted file, permission denied).
	Load() (*oauth2.Token, error)

	// Clear removes any stored token. Idempotent: Clear on an empty store
	// returns nil.
	Clear() error
}

// FileTokenStore persists a token to a JSON file on disk. The file is
// written with mode 0600 and its parent directory is created with mode 0700
// if missing — this stays close to common practice for credential files
// (gcloud, gh, kubectl). The on-disk shape is whatever encoding/json emits
// for *oauth2.Token (access_token, refresh_token, token_type, expiry).
type FileTokenStore struct {
	Path string
}

// NewFileTokenStore returns a FileTokenStore writing to path. path must be
// absolute or relative to the caller's working directory; no expansion of
// "~" is performed.
//
// The parent directory is not created here — that happens lazily on Save,
// so a read-only environment can still call Load without side effects.
func NewFileTokenStore(path string) (*FileTokenStore, error) {
	if path == "" {
		return nil, errors.New("authkit: FileTokenStore path is empty")
	}
	return &FileTokenStore{Path: path}, nil
}

// DefaultFileTokenStorePath returns the canonical CLI / desktop token file
// path, "<UserConfigDir>/latere/token.json", matching the location historical
// latere-cli builds have used. Callers that want a non-standard path
// should construct their own with NewFileTokenStore.
func DefaultFileTokenStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("authkit: locate user config dir: %w", err)
	}
	return filepath.Join(dir, "latere", "token.json"), nil
}

func (s *FileTokenStore) Save(t *oauth2.Token) error {
	if t == nil {
		return errors.New("authkit: Save nil token")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("authkit: create token dir: %w", err)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("authkit: marshal token: %w", err)
	}
	// Write to a sibling tempfile then rename for atomicity. Avoids
	// truncating the existing file on a partial write.
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("authkit: create temp token: %w", err)
	}
	tmpPath := tmp.Name()
	cleaned := false
	defer func() {
		if !cleaned {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("authkit: chmod temp token: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("authkit: write temp token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("authkit: close temp token: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("authkit: rename token: %w", err)
	}
	cleaned = true
	return nil
}

func (s *FileTokenStore) Load() (*oauth2.Token, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("authkit: read token: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var t oauth2.Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("authkit: parse token: %w", err)
	}
	return &t, nil
}

func (s *FileTokenStore) Clear() error {
	if err := os.Remove(s.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("authkit: remove token: %w", err)
	}
	return nil
}
