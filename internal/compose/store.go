package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var validProjectName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Storer interface {
	Exists(name string) bool
	Create(name, content string) error
	Read(name string) (string, error)
	Write(name, content string) error
	Delete(name string) error
	List() ([]string, error)
	FilePath(name string) string
}

// Project names are validated to [a-zA-Z0-9_-]{1,64} to prevent path traversal.
type Store struct {
	baseDir string
}

var _ Storer = (*Store)(nil)

func NewStore(configDir string) *Store {
	return &Store{baseDir: filepath.Join(configDir, "compose")}
}

func ValidateName(name string) error {
	if !validProjectName.MatchString(name) {
		return fmt.Errorf("invalid stack name %q: must match [a-zA-Z0-9_-]{1,64}", name)
	}
	return nil
}

func (s *Store) FilePath(name string) string {
	return filepath.Join(s.baseDir, name, "docker-compose.yml")
}

func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.FilePath(name))
	return err == nil
}

func (s *Store) Create(name, content string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if s.Exists(name) {
		return fmt.Errorf("stack %q already exists", name)
	}
	dir := filepath.Dir(s.FilePath(name))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create stack directory: %w", err)
	}
	return os.WriteFile(s.FilePath(name), []byte(content), 0640)
}

func (s *Store) Read(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.FilePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("stack %q not found", name)
		}
		return "", fmt.Errorf("failed to read compose file: %w", err)
	}
	return string(data), nil
}

func (s *Store) Write(name, content string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !s.Exists(name) {
		return fmt.Errorf("stack %q not found", name)
	}
	return os.WriteFile(s.FilePath(name), []byte(content), 0640)
}

func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.baseDir, name))
}

func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list compose stacks: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !validProjectName.MatchString(name) {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.baseDir, name, "docker-compose.yml")); err == nil {
			names = append(names, name)
		}
	}
	return names, nil
}
