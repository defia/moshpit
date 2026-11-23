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
	"errors"
	"strings"

	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type sftpFindResult struct {
	directory, name string
	isDirectory     bool
}

func (b *sftpBrowser) findFiles() {
	p := b.active
	if b.busy || p.dir == "" {
		return
	}
	b.promptText("Find names under "+p.label, "Contains: ", "", func(value string) error {
		if value == "" {
			return errors.New("enter part of a filename")
		}
		b.restore()
		start := p.dir
		b.run("Searching "+p.label+"…", func() func() {
			results, limited, inaccessible := findSFTPNames(b.session.Files(p.remote), p.join, start, strings.ToLower(value), b.done)
			return func() {
				note := ""
				if limited {
					note = " · Limit reached"
				}
				if inaccessible {
					note += " · Some folders could not be read"
				}
				if len(results) == 0 {
					b.setStatus("No matching names"+note, ActiveTheme.Peach)
					return
				}
				actions := make([]sftpAction, 0, len(results))
				for _, result := range results {
					fullPath := p.join(result.directory, result.name)
					dir := result.directory
					if result.isDirectory {
						dir = fullPath
					}
					actions = append(actions, sftpAction{fullPath, func() { b.openDirectory(p, dir) }})
				}
				b.actionMenu("Find results"+note, actions)
			}
		})
		return nil
	})
}

func findSFTPNames(files ports.SFTPFileSystem, join func(string, string) string, root, query string, done <-chan struct{}) (results []sftpFindResult, limited, inaccessible bool) {
	const maxFolders, maxResults = 10000, 500
	pending := []string{root}
	for visited := 0; len(pending) > 0; visited++ {
		if isClosed(done) {
			break
		}
		if visited == maxFolders {
			return results, true, inaccessible
		}
		last := len(pending) - 1
		dir := pending[last]
		pending = pending[:last]
		entries, err := files.ReadDir(dir)
		if err != nil {
			inaccessible = true
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !validSFTPName(name) {
				continue
			}
			if strings.Contains(strings.ToLower(name), query) {
				if len(results) == maxResults {
					return results, true, inaccessible
				}
				results = append(results, sftpFindResult{dir, name, entry.IsDir()})
			}
			if entry.IsDir() {
				pending = append(pending, join(dir, name))
			}
		}
	}
	return results, false, inaccessible
}
