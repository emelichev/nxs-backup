package mysql_physical

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDefaultsFile(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "server.cnf")
		if err := os.WriteFile(filePath, []byte("[mysqld]\n"), 0400); err != nil {
			t.Fatalf("write defaults file: %v", err)
		}
		if err := validateDefaultsFile(filePath); err != nil {
			t.Fatalf("validateDefaultsFile() error = %v", err)
		}
	})

	t.Run("symlink to regular file", func(t *testing.T) {
		tempDir := t.TempDir()
		targetPath := filepath.Join(tempDir, "server.cnf")
		linkPath := filepath.Join(tempDir, "server-link.cnf")
		if err := os.WriteFile(targetPath, []byte("[mysqld]\n"), 0400); err != nil {
			t.Fatalf("write defaults file: %v", err)
		}
		if err := os.Symlink(targetPath, linkPath); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		if err := validateDefaultsFile(linkPath); err != nil {
			t.Fatalf("validateDefaultsFile() error = %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "missing.cnf")
		err := validateDefaultsFile(filePath)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validateDefaultsFile() error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		err := validateDefaultsFile(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("validateDefaultsFile() error = %v, want non-regular-file error", err)
		}
	})

	t.Run("broken symlink", func(t *testing.T) {
		tempDir := t.TempDir()
		linkPath := filepath.Join(tempDir, "broken.cnf")
		if err := os.Symlink(filepath.Join(tempDir, "missing.cnf"), linkPath); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		err := validateDefaultsFile(linkPath)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validateDefaultsFile() error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestInitRejectsInvalidDefaultsFileBeforeToolChecks(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.cnf")
	_, err := Init(JobParams{
		Name: "physical-job",
		Sources: []SourceParams{
			{
				Name:         "mysql-source",
				DefaultsFile: missingPath,
			},
		},
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Init() error = %v, want os.ErrNotExist", err)
	}
	for _, value := range []string{"physical-job", "mysql-source", "defaults_file", missingPath} {
		if !strings.Contains(err.Error(), value) {
			t.Fatalf("Init() error %q does not contain %q", err, value)
		}
	}
}
