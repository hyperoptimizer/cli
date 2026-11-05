package fileio

import (
	"bytes"
	"errors"
	"github.com/natefinch/atomic"
	"os"
)

// WriteProtected narrows existing permissions before atomic.WriteFile, which
// deliberately inherits the destination's mode. New temporary files use 0600.
func WriteProtected(path string, data []byte) error {
	stat, err := os.Lstat(path)
	if err == nil {
		if !stat.Mode().IsRegular() {
			return errors.New("protected file must be a regular file")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomic.WriteFile(path, bytes.NewReader(data))
}
