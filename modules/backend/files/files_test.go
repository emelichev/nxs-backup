package files

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/ini.v1"
)

func TestCreateTmpMysqlOptionFile(t *testing.T) {
	t.Run("writes closes and restricts file", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath, err := createTmpMysqlOptionFile(tempDir, func(file io.Writer) error {
			_, err := io.WriteString(file, "secret")
			return err
		})
		if err != nil {
			t.Fatalf("createTmpMysqlOptionFile() error = %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(filePath) })

		if filepath.Dir(filePath) != tempDir {
			t.Fatalf("temporary file directory = %q, want %q", filepath.Dir(filePath), tempDir)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read temporary file: %v", err)
		}
		if string(content) != "secret" {
			t.Fatalf("temporary file content = %q, want %q", content, "secret")
		}
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatalf("stat temporary file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0400 {
			t.Fatalf("temporary file mode = %04o, want 0400", got)
		}

		file, err := os.OpenFile(filePath, os.O_WRONLY, 0)
		if err == nil {
			_ = file.Close()
			if os.Geteuid() != 0 {
				t.Fatal("temporary file can still be opened for writing")
			}
		}
	})

	t.Run("removes partial file after write error", func(t *testing.T) {
		tempDir := t.TempDir()
		writeErr := errors.New("injected write failure")
		filePath, err := createTmpMysqlOptionFile(tempDir, func(file io.Writer) error {
			if _, err := io.WriteString(file, "partial"); err != nil {
				return err
			}
			return writeErr
		})
		if !errors.Is(err, writeErr) {
			t.Fatalf("createTmpMysqlOptionFile() error = %v, want %v", err, writeErr)
		}
		if filePath != "" {
			t.Fatalf("createTmpMysqlOptionFile() path = %q after error, want empty", filePath)
		}
		assertDirectoryEmpty(t, tempDir)
	})

	t.Run("returns close error and removes file", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath, err := createTmpMysqlOptionFile(tempDir, func(writer io.Writer) error {
			file, ok := writer.(*os.File)
			if !ok {
				t.Fatalf("writer type = %T, want *os.File", writer)
			}
			if _, err := io.WriteString(file, "complete"); err != nil {
				return err
			}
			return file.Close()
		})
		if err == nil || !strings.Contains(err.Error(), "close temporary MySQL option file") {
			t.Fatalf("createTmpMysqlOptionFile() error = %v, want close error", err)
		}
		if filePath != "" {
			t.Fatalf("createTmpMysqlOptionFile() path = %q after close error, want empty", filePath)
		}
		assertDirectoryEmpty(t, tempDir)
	})

	t.Run("creates unique paths", func(t *testing.T) {
		tempDir := t.TempDir()
		firstPath, err := createTmpMysqlOptionFile(tempDir, func(io.Writer) error { return nil })
		if err != nil {
			t.Fatalf("first createTmpMysqlOptionFile() error = %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(firstPath) })
		secondPath, err := createTmpMysqlOptionFile(tempDir, func(io.Writer) error { return nil })
		if err != nil {
			t.Fatalf("second createTmpMysqlOptionFile() error = %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(secondPath) })
		if firstPath == secondPath {
			t.Fatalf("temporary file paths are equal: %q", firstPath)
		}
	})
}

func TestCreateTmpMysqlFiles(t *testing.T) {
	auth := ini.Empty()
	section, err := auth.NewSection("xtrabackup")
	if err != nil {
		t.Fatalf("create auth section: %v", err)
	}
	if _, err := section.NewKey("user", "backup"); err != nil {
		t.Fatalf("create user key: %v", err)
	}
	if _, err := section.NewKey("password", "secret"); err != nil {
		t.Fatalf("create password key: %v", err)
	}

	t.Run("auth file", func(t *testing.T) {
		filePath, err := CreateTmpMysqlAuthFile(auth)
		if err != nil {
			t.Fatalf("CreateTmpMysqlAuthFile() error = %v", err)
		}
		t.Cleanup(func() { _ = DeleteTmpMysqlAuthFile(filePath) })
		assertMysqlOptionFile(t, filePath, "", "[xtrabackup]", "user     = backup", "password = secret")
	})

	t.Run("merged defaults file", func(t *testing.T) {
		srcPath := filepath.Join(t.TempDir(), "server.cnf")
		const src = "[mysqld]\ndatadir=/var/lib/mysql-data\n"
		if err := os.WriteFile(srcPath, []byte(src), 0600); err != nil {
			t.Fatalf("write source defaults file: %v", err)
		}

		filePath, err := CreateTmpMysqlDefaultsFile(srcPath, auth)
		if err != nil {
			t.Fatalf("CreateTmpMysqlDefaultsFile() error = %v", err)
		}
		t.Cleanup(func() { _ = DeleteTmpMysqlAuthFile(filePath) })
		assertMysqlOptionFile(t, filePath, src, "[xtrabackup]", "user     = backup", "password = secret")
	})

	t.Run("missing defaults source", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "missing.cnf")
		filePath, err := CreateTmpMysqlDefaultsFile(missingPath, auth)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CreateTmpMysqlDefaultsFile() error = %v, want os.ErrNotExist", err)
		}
		if filePath != "" {
			t.Fatalf("CreateTmpMysqlDefaultsFile() path = %q after error, want empty", filePath)
		}
	})
}

func assertDirectoryEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory contains %d entries after failure: %v", len(entries), entries)
	}
}

func assertMysqlOptionFile(t *testing.T, filePath, prefix string, contains ...string) {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read MySQL option file: %v", err)
	}
	if !strings.HasPrefix(string(content), prefix) {
		t.Fatalf("MySQL option file does not start with source content: %q", content)
	}
	for _, value := range contains {
		if !strings.Contains(string(content), value) {
			t.Fatalf("MySQL option file content %q does not contain %q", content, value)
		}
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat MySQL option file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0400 {
		t.Fatalf("MySQL option file mode = %04o, want 0400", got)
	}
}
