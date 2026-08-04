package files

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/juju/ratelimit"
	"gopkg.in/ini.v1"
)

type limitedWriteCloser struct {
	w io.Writer
	c io.Closer
}

type LimitedReadCloser struct {
	r io.Reader
	c io.Closer
	s io.Seeker
}

func (lwc *limitedWriteCloser) Write(p []byte) (int, error) {
	return lwc.w.Write(p)
}

func (lwc *limitedWriteCloser) Close() error {
	return lwc.c.Close()
}

func (lrc *LimitedReadCloser) Read(p []byte) (int, error) {
	return lrc.r.Read(p)
}

func (lrc *LimitedReadCloser) Close() error {
	return lrc.c.Close()
}

func (lrc *LimitedReadCloser) Seek(offset int64, whence int) (int64, error) {
	return lrc.s.Seek(offset, whence)
}

func CreateTmpMysqlAuthFile(af *ini.File) (authFile string, err error) {
	return createTmpMysqlOptionFile("/tmp", func(file io.Writer) error {
		_, err := af.WriteTo(file)
		return err
	})
}

// CreateTmpMysqlDefaultsFile writes a single MySQL option file that combines the
// user-provided defaults file (e.g. the server my.cnf, needed for datadir and
// other server settings) with the connection credentials section, and returns
// its path. It is passed to xtrabackup/mariadb-backup as a single
// `--defaults-file`: these tools accept only one leading "defaults" option and
// it must come first, so credentials cannot be supplied via a separate
// `--defaults-extra-file` when `--defaults-file` is used.
func CreateTmpMysqlDefaultsFile(srcDefaultsFile string, af *ini.File) (defaultsFile string, err error) {
	src, err := os.ReadFile(srcDefaultsFile)
	if err != nil {
		return "", fmt.Errorf("read MySQL defaults file %q: %w", srcDefaultsFile, err)
	}

	return createTmpMysqlOptionFile("/tmp", func(file io.Writer) error {
		// Inline the user's defaults file first (keeps its groups, e.g.
		// [mysqld] datadir), then append the credentials group.
		if _, err := file.Write(src); err != nil {
			return err
		}
		if _, err := io.WriteString(file, "\n"); err != nil {
			return err
		}
		_, err := af.WriteTo(file)
		return err
	})
}

// createTmpMysqlOptionFile creates a private, uniquely named MySQL option file.
// The file is returned only after its contents have been written, it has been
// closed successfully, and its mode has been restricted to read-only. Any
// failure removes the incomplete file before returning.
func createTmpMysqlOptionFile(tempDir string, writeContent func(io.Writer) error) (filePath string, err error) {
	file, err := os.CreateTemp(tempDir, "nxs-backup-mysql-")
	if err != nil {
		return "", fmt.Errorf("create temporary MySQL option file: %w", err)
	}

	filePath = file.Name()
	closed := false
	keep := false
	defer func() {
		if !closed {
			closeErr := file.Close()
			if closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close temporary MySQL option file %q: %w", filePath, closeErr))
			}
		}
		if !keep {
			removeErr := os.Remove(filePath)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove temporary MySQL option file %q: %w", filePath, removeErr))
			}
			filePath = ""
		}
	}()

	if err = writeContent(file); err != nil {
		err = fmt.Errorf("write temporary MySQL option file %q: %w", filePath, err)
		return
	}

	err = file.Close()
	closed = true
	if err != nil {
		err = fmt.Errorf("close temporary MySQL option file %q: %w", filePath, err)
		return
	}

	if err = os.Chmod(filePath, 0400); err != nil {
		err = fmt.Errorf("set permissions on temporary MySQL option file %q: %w", filePath, err)
		return
	}

	keep = true
	return
}

func DeleteTmpMysqlAuthFile(path string) error {
	return os.RemoveAll(path)
}

func GetLimitedFileWriter(filePath string, rateLim int64) (io.WriteCloser, error) {
	file, err := os.Create(filePath)
	if err != nil {
		return nil, err
	}

	lwc := &limitedWriteCloser{
		c: file,
	}
	if rateLim != 0 {
		bucket := ratelimit.NewBucketWithRate(float64(rateLim), rateLim*2)
		lwc.w = ratelimit.Writer(file, bucket)
	} else {
		lwc.w = file
	}

	return lwc, nil
}

func GetLimitedFileReader(filePath string, rateLim int64) (*LimitedReadCloser, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	lrc := &LimitedReadCloser{
		c: file,
		s: file,
	}
	if rateLim != 0 {
		bucket := ratelimit.NewBucketWithRate(float64(rateLim), rateLim*2)
		lrc.r = ratelimit.Reader(file, bucket)
	} else {
		lrc.r = file
	}

	return lrc, err
}
