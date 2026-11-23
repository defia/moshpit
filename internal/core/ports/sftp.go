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

package ports

import (
	"context"
	"io/fs"
)

// SFTPFileSystem exposes the same operations to either side of the browser.
type SFTPFileSystem interface {
	ReadDir(string) ([]fs.FileInfo, error)
	Lstat(string) (fs.FileInfo, error)
	Mkdir(string) error
	CreateEmpty(string) error
	Rename(from, to string) error
	Remove(context.Context, string) error
	Chmod(string, fs.FileMode) error
	ReadPreview(string, int64) ([]byte, error)
}

type SFTPLocation struct {
	Path   string
	Remote bool
}

type SFTPTransfer struct {
	Source, Destination SFTPLocation
	Replace             bool
}

type SFTPProgress struct {
	Path                       string
	Bytes, FileBytes, FileSize int64
	Files                      int
}

// SFTPSession is an authenticated file-transfer session with one SSH host.
type SFTPSession interface {
	Getwd() (string, error)
	Files(remote bool) SFTPFileSystem
	Transfer(context.Context, SFTPTransfer, func(SFTPProgress)) (int64, error)
	Close()
}
