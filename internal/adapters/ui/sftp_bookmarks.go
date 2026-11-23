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

package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type sftpBookmark struct {
	Host string `json:"host"`
	ports.SFTPLocation
}

func readSFTPBookmarks() (string, []sftpBookmark, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	filename := filepath.Join(home, ".moshpit", "sftp-bookmarks.json")
	// #nosec G304 -- the file is inside the existing per-user moshpit directory.
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return filename, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var bookmarks []sftpBookmark
	err = json.Unmarshal(data, &bookmarks)
	return filename, bookmarks, err
}

func writeSFTPBookmarks(filename string, bookmarks []sftpBookmark) error {
	data, err := json.MarshalIndent(bookmarks, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "sftp-bookmarks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err := errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filename)
}

func (b *sftpBrowser) bookmarks() {
	if b.busy || b.active.dir == "" {
		return
	}
	p := b.active
	_, bookmarks, err := readSFTPBookmarks()
	if err != nil {
		b.setStatus("Could not read bookmarks: "+err.Error(), ActiveTheme.Red)
		return
	}
	current := sftpBookmark{
		Host:         b.alias,
		SFTPLocation: ports.SFTPLocation{Path: p.dir, Remote: p.remote},
	}
	var actions []sftpAction
	if slices.Contains(bookmarks, current) {
		actions = append(actions, sftpAction{"Remove bookmark for this directory", func() { b.updateBookmark(current, true) }})
	} else {
		actions = append(actions, sftpAction{"Bookmark this directory", func() { b.updateBookmark(current, false) }})
	}
	for _, mark := range bookmarks {
		if mark.Host == b.alias && mark.Remote == p.remote {
			actions = append(actions, sftpAction{mark.Path, func() { b.openDirectory(p, mark.Path) }})
		}
	}
	b.actionMenu(p.label+" bookmarks", actions)
}

func (b *sftpBrowser) updateBookmark(mark sftpBookmark, remove bool) {
	file, bookmarks, err := readSFTPBookmarks()
	if err == nil {
		if remove {
			bookmarks = slices.DeleteFunc(bookmarks, func(existing sftpBookmark) bool { return existing == mark })
		} else if !slices.Contains(bookmarks, mark) {
			bookmarks = append(bookmarks, mark)
		}
		err = writeSFTPBookmarks(file, bookmarks)
	}
	if err != nil {
		b.setStatus("Could not save bookmarks: "+err.Error(), ActiveTheme.Red)
		return
	}
	b.setStatus("Bookmarks updated", ActiveTheme.Green)
}
