// Copyright 2025.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type browserFileSystem interface {
	ports.SFTPFileSystem
	join(string, string) string
	dir(string) string
	openRegular(string) (io.ReadCloser, error)
	create(string) (io.WriteCloser, error)
	rename(string, string, bool) error
	remove(string, bool) error
	chtimes(string, time.Time) error
}

func (s *sftpSession) Files(remote bool) ports.SFTPFileSystem { return s.files(remote) }

func (s *sftpSession) files(remote bool) browserFileSystem {
	if remote {
		return remoteBrowserFS{s.client}
	}
	return localBrowserFS{}
}

type localBrowserFS struct{}

func (localBrowserFS) join(dir, name string) string           { return filepath.Join(dir, name) }
func (localBrowserFS) dir(name string) string                 { return filepath.Dir(name) }
func (localBrowserFS) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }
func (localBrowserFS) Mkdir(name string) error                { return os.Mkdir(name, 0o755) }
func (localBrowserFS) Chmod(name string, mode fs.FileMode) error {
	return chmodBrowserFile(localBrowserFS{}, name, mode, os.Chmod)
}
func (localBrowserFS) chtimes(name string, modified time.Time) error {
	return os.Chtimes(name, modified, modified)
}

func (localBrowserFS) ReadDir(dir string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	info := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		file, err := entry.Info()
		if err != nil {
			return nil, err
		}
		info = append(info, file)
	}
	return info, nil
}

func (localBrowserFS) openRegular(name string) (io.ReadCloser, error) {
	return openRegularBrowserFile(localBrowserFS{}, name, func(name string) (browserFile, error) {
		// #nosec G304 -- the path comes from a user's local file selection.
		return os.Open(name)
	})
}

func (localBrowserFS) create(name string) (io.WriteCloser, error) {
	// #nosec G304 -- the staging filename is generated in the selected destination.
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func (f localBrowserFS) CreateEmpty(name string) error { return createEmptyBrowserFile(f, name) }

func (f localBrowserFS) Rename(from, to string) error { return f.rename(from, to, false) }

func (localBrowserFS) rename(from, to string, replace bool) error {
	if replace {
		return os.Rename(from, to)
	}
	return renameLocalWithoutReplace(from, to)
}

func (localBrowserFS) remove(name string, _ bool) error { return os.Remove(name) }

func (f localBrowserFS) Remove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || !safeBrowserName(filepath.Base(name)) {
		return errors.New("refusing to remove a filesystem root")
	}
	if _, err := f.Lstat(name); err != nil {
		return err
	}
	return os.RemoveAll(name)
}

func (f localBrowserFS) ReadPreview(name string, limit int64) ([]byte, error) {
	return readBrowserPreview(f, name, limit)
}

type remoteBrowserFS struct{ client *sftp.Client }

func (remoteBrowserFS) join(dir, name string) string                 { return path.Join(dir, name) }
func (remoteBrowserFS) dir(name string) string                       { return path.Dir(name) }
func (f remoteBrowserFS) Lstat(name string) (fs.FileInfo, error)     { return f.client.Lstat(name) }
func (f remoteBrowserFS) Mkdir(name string) error                    { return f.client.Mkdir(name) }
func (f remoteBrowserFS) ReadDir(name string) ([]fs.FileInfo, error) { return f.client.ReadDir(name) }
func (f remoteBrowserFS) Chmod(name string, mode fs.FileMode) error {
	return chmodBrowserFile(f, name, mode, f.client.Chmod)
}
func (f remoteBrowserFS) chtimes(name string, modified time.Time) error {
	return f.client.Chtimes(name, modified, modified)
}

func (f remoteBrowserFS) openRegular(name string) (io.ReadCloser, error) {
	return openRegularBrowserFile(f, name, func(name string) (browserFile, error) {
		return f.client.Open(name)
	})
}

func (f remoteBrowserFS) create(name string) (io.WriteCloser, error) {
	return f.client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}

func (f remoteBrowserFS) CreateEmpty(name string) error { return createEmptyBrowserFile(f, name) }

func (f remoteBrowserFS) Rename(from, to string) error { return f.rename(from, to, false) }

func (f remoteBrowserFS) rename(from, to string, replace bool) error {
	if replace {
		if _, supported := f.client.HasExtension("posix-rename@openssh.com"); !supported {
			return errors.New("the server does not support atomic replacement; choose a different name")
		}
		return f.client.PosixRename(from, to)
	}
	if err := requireMissing(f, to); err != nil {
		return err
	}
	return f.client.Rename(from, to)
}

func (f remoteBrowserFS) remove(name string, directory bool) error {
	if directory {
		return f.client.RemoveDirectory(name)
	}
	return f.client.Remove(name)
}

func (f remoteBrowserFS) Remove(ctx context.Context, name string) error {
	return removeBrowserTree(ctx, f, name)
}

func (f remoteBrowserFS) ReadPreview(name string, limit int64) ([]byte, error) {
	return readBrowserPreview(f, name, limit)
}

func createEmptyBrowserFile(f browserFileSystem, name string) error {
	if err := requireMissing(f, name); err != nil {
		return err
	}
	file, err := f.create(name)
	if err != nil {
		return err
	}
	return file.Close()
}

type browserFile interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

func openRegularBrowserFile(f browserFileSystem, name string, open func(string) (browserFile, error)) (io.ReadCloser, error) {
	entry, err := f.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !entry.Mode().IsRegular() {
		return nil, errors.New("select a regular file")
	}
	file, err := open(name)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	switch {
	case err != nil:
	case !actual.Mode().IsRegular():
		err = errors.New("the source is no longer a regular file")
	case entry.Sys() != nil && actual.Sys() != nil && isLocalFileSystem(f) && !os.SameFile(entry, actual):
		err = errors.New("the file changed before it could be opened")
	default:
		return file, nil
	}
	_ = file.Close()
	return nil, err
}

func isLocalFileSystem(f browserFileSystem) bool {
	_, ok := f.(localBrowserFS)
	return ok
}

func readBrowserPreview(f browserFileSystem, name string, limit int64) ([]byte, error) {
	file, err := f.openRegular(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func chmodBrowserFile(f browserFileSystem, name string, mode fs.FileMode, chmod func(string, fs.FileMode) error) error {
	entry, err := f.Lstat(name)
	if err != nil {
		return err
	}
	if !entry.IsDir() && !entry.Mode().IsRegular() {
		return errors.New("permissions are available for regular files and directories")
	}
	return chmod(name, mode.Perm())
}

func requireMissing(f browserFileSystem, name string) error {
	_, err := f.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil:
		return fs.ErrExist
	default:
		return err
	}
}

func safeBrowserName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name && filepath.IsLocal(name)
}

func removeBrowserTree(ctx context.Context, f browserFileSystem, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || name == "." || name == ".." || name == f.dir(name) {
		return errors.New("refusing to remove a filesystem root")
	}
	info, err := f.Lstat(name)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := f.ReadDir(name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !safeBrowserName(entry.Name()) {
				return fmt.Errorf("directory contains an unsupported filename: %q", entry.Name())
			}
			if err := removeBrowserTree(ctx, f, f.join(name, entry.Name())); err != nil {
				return err
			}
		}
	}
	return f.remove(name, info.IsDir())
}
