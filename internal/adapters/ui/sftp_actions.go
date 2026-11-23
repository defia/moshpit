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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type sftpAction struct {
	label string
	run   func()
}

func (b *sftpBrowser) restore() {
	b.app.SetRoot(b.root, true).SetFocus(b.active.list)
}

func (b *sftpBrowser) actionMenu(title string, actions []sftpAction) {
	list := tview.NewList().ShowSecondaryText(false).SetUseStyleTags(false, false)
	list.SetBorder(true).SetTitle(" " + tview.Escape(displaySFTPText(title)) + " ")
	list.SetDoneFunc(b.restore)
	for _, action := range actions {
		list.AddItem(displaySFTPText(action.label), "", 0, func() {
			b.restore()
			action.run()
		})
	}
	list.AddItem("Back", "", 0, b.restore)
	b.app.SetRoot(list, true)
}

func (b *sftpBrowser) actions() {
	if b.busy {
		return
	}
	actions := []sftpAction{
		{"Preview text   p", b.preview},
		{"Properties", b.properties},
		{"Permissions (chmod)", b.changePermissions},
		{"New directory   n", b.newDirectory},
		{"New empty file", b.newFile},
		{"Rename   F2", func() { b.renameOrMove(false) }},
		{"Move to path", func() { b.renameOrMove(true) }},
		{"Duplicate here", b.duplicate},
		{"Delete   Del", b.deleteSelected},
		{"Go to path   g", b.goToPath},
		{"Go to home", b.goHome},
		{"Previous directory", func() { b.history(false) }},
		{"Next directory", func() { b.history(true) }},
		{"Bookmarks   b", b.bookmarks},
		{"Filter by name   /", b.filterMenu},
		{"Find by name in subdirectories", b.findFiles},
		{"Sort   s", b.sortMenu},
		{"Toggle hidden files   h", b.toggleHidden},
		{"Compare displayed directories", b.compareDirectories},
		{"Refresh both panes   r", b.refresh},
	}
	if b.lastTransfer != nil {
		actions = append(actions, sftpAction{"Retry last transfer", func() { b.startTransfer(*b.lastTransfer) }})
	}
	b.actionMenu(b.active.label+" • File actions", actions)
}

func (b *sftpBrowser) promptText(title, label, initial string, submit func(string) error) {
	field := tview.NewInputField().SetLabel(label).SetText(initial).SetFieldWidth(44)
	form := tview.NewForm().AddFormItem(field)
	form.SetBorder(true).SetTitle(" " + tview.Escape(displaySFTPText(title)) + " ")
	status := tview.NewTextView()
	form.AddButton("OK", func() {
		if err := submit(field.GetText()); err != nil {
			status.SetText(displaySFTPText(err.Error()))
			b.app.SetFocus(field)
		}
	})
	form.AddButton("Cancel", b.restore).SetCancelFunc(b.restore)
	panel := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 7, 0, true).AddItem(status, 2, 0, false)
	b.app.SetRoot(panel, true)
}

func (b *sftpBrowser) showText(title, contents string) {
	text := tview.NewTextView().SetScrollable(true).SetWordWrap(true).SetDynamicColors(false).SetText(contents)
	text.SetBorder(true).SetTitleAlign(tview.AlignLeft).SetTitle(" " + tview.Escape(displaySFTPText(title)) + " ")
	text.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEscape {
			b.restore()
		}
	})
	panel := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(text, 0, 1, true).
		AddItem(tview.NewButton("Esc Back").SetSelectedFunc(b.restore), 1, 0, false)
	panel.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			b.restore()
			return nil
		}
		return event
	})
	b.app.SetRoot(panel, true)
}

func (b *sftpBrowser) openSelected() {
	if b.busy {
		return
	}
	p := b.active
	if entry, ok := p.selected(); ok {
		if entry.mode.IsDir() {
			b.openDirectory(p, p.join(p.dir, entry.name))
		} else {
			b.preview()
		}
	}
}

func (b *sftpBrowser) upDirectory() {
	b.openDirectory(b.active, b.active.join(b.active.dir, ".."))
}

func (b *sftpBrowser) history(forward bool) {
	p := b.active
	previous, next := &p.back, &p.forward
	if forward {
		previous, next = next, previous
	}
	if b.busy || len(*previous) == 0 {
		return
	}
	index := len(*previous) - 1
	b.navigate(p, (*previous)[index], func() {
		*next = append(*next, p.dir)
		*previous = (*previous)[:index]
	})
}

func (b *sftpBrowser) home(p *sftpPanel) string {
	if p.remote {
		return b.remoteHome
	}
	home, _ := os.UserHomeDir()
	return home
}

func (b *sftpBrowser) goHome() {
	if home := b.home(b.active); home != "" {
		b.openDirectory(b.active, home)
	}
}

func (b *sftpBrowser) resolveUserPath(p *sftpPanel, value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("enter a path")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home := b.home(p); home != "" {
			if value == "~" {
				return home, nil
			}
			return p.join(home, value[2:]), nil
		}
		return "", errors.New("home directory is unavailable")
	}
	if p.remote && path.IsAbs(value) {
		return path.Clean(value), nil
	}
	if !p.remote && filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return p.join(p.dir, value), nil
}

func (b *sftpBrowser) goToPath() {
	p := b.active
	if b.busy || p.dir == "" {
		return
	}
	b.promptText("Go to "+p.label+" directory", "Path: ", p.dir, func(value string) error {
		dir, err := b.resolveUserPath(p, value)
		if err != nil {
			return err
		}
		b.restore()
		b.openDirectory(p, dir)
		return nil
	})
}

func (b *sftpBrowser) toggleHidden() {
	if b.busy {
		return
	}
	p := b.active
	p.showHidden = !p.showHidden
	p.update(p.dir, p.files)
	if p.showHidden {
		b.setStatus("Showing hidden files in "+p.label, ActiveTheme.Text)
	} else {
		b.setStatus("Hiding dotfiles in "+p.label, ActiveTheme.Text)
	}
}

func (b *sftpBrowser) sortMenu() {
	p := b.active
	if b.busy {
		return
	}
	var actions []sftpAction
	for index, label := range []string{"Name (A–Z)", "Name (Z–A)", "Size (largest first)", "Modified (newest first)"} {
		actions = append(actions, sftpAction{label, func() {
			p.sortBy = index
			p.update(p.dir, p.files)
			b.setStatus("Sorted by "+label, ActiveTheme.Text)
		}})
	}
	b.actionMenu("Sort "+p.label, actions)
}

func (b *sftpBrowser) filterMenu() {
	p := b.active
	if b.busy {
		return
	}
	b.promptText("Filter "+p.label+" by name", "Contains: ", p.filter, func(value string) error {
		b.restore()
		p.filter = value
		p.update(p.dir, p.files)
		if value == "" {
			b.setStatus("Filter cleared", ActiveTheme.Text)
		} else {
			b.setStatus("Filter: "+value, ActiveTheme.Text)
		}
		return nil
	})
}

func (b *sftpBrowser) currentSelection() (*sftpPanel, sftpEntry, bool) {
	p := b.active
	if b.busy || p.dir == "" {
		return p, sftpEntry{}, false
	}
	entry, ok := p.selected()
	if !ok || !validSFTPName(entry.name) {
		b.setStatus("Select a file or directory in "+p.label, ActiveTheme.Peach)
		return p, sftpEntry{}, false
	}
	return p, entry, true
}

func (b *sftpBrowser) fileOperation(p *sftpPanel, message string, operation func(ports.SFTPFileSystem) error) {
	dir := p.dir
	b.run(message+"…", func() func() {
		operationErr := operation(b.session.Files(p.remote))
		dir, entries, err := b.loadDirectory(p.remote, dir)
		return func() {
			if err == nil {
				p.update(dir, entries)
			}
			if operationErr != nil {
				b.setStatus(message+" failed: "+operationErr.Error(), ActiveTheme.Red)
				return
			}
			if err != nil {
				b.setStatus(message+" completed · Refresh failed: "+err.Error(), ActiveTheme.Peach)
				return
			}
			b.setStatus(message+" completed", ActiveTheme.Green)
		}
	})
}

func (b *sftpBrowser) newDirectory() {
	p := b.active
	if b.busy || p.dir == "" {
		return
	}
	b.promptText("New directory in "+p.label, "Name: ", "", func(name string) error {
		if !validSFTPName(name) {
			return errors.New("enter a single directory name")
		}
		b.restore()
		target := p.join(p.dir, name)
		b.fileOperation(p, "Create "+name, func(files ports.SFTPFileSystem) error { return files.Mkdir(target) })
		return nil
	})
}

func (b *sftpBrowser) newFile() {
	p := b.active
	if b.busy || p.dir == "" {
		return
	}
	b.promptText("New empty file in "+p.label, "Name: ", "", func(name string) error {
		if !validSFTPName(name) {
			return errors.New("enter a single filename")
		}
		b.restore()
		target := p.join(p.dir, name)
		b.fileOperation(p, "Create "+name, func(files ports.SFTPFileSystem) error { return files.CreateEmpty(target) })
		return nil
	})
}

func (b *sftpBrowser) renameOrMove(move bool) {
	p, entry, ok := b.currentSelection()
	if !ok {
		return
	}
	from := p.join(p.dir, entry.name)
	title, label, initial := "Rename "+entry.name, "New name: ", entry.name
	if move {
		title, label, initial = "Move "+entry.name, "Destination path: ", from
	}
	b.promptText(title, label, initial, func(value string) error {
		if !move && !validSFTPName(value) {
			return errors.New("enter a single filename")
		}
		to := p.join(p.dir, value)
		if move {
			var err error
			to, err = b.resolveUserPath(p, value)
			if err != nil {
				return err
			}
		}
		if to == from {
			return errors.New("enter a different path")
		}
		b.restore()
		b.fileOperation(p, title, func(files ports.SFTPFileSystem) error { return files.Rename(from, to) })
		return nil
	})
}

func (b *sftpBrowser) deleteSelected() {
	p, entry, ok := b.currentSelection()
	if !ok {
		return
	}
	target := p.join(p.dir, entry.name)
	message := "Delete " + p.label + " file?\n\n" + displaySFTPText(target)
	if entry.mode.IsDir() {
		message = "Delete this " + p.label + " directory and everything inside it?\n\n" + displaySFTPText(target)
	}
	modal := tview.NewModal().SetText(tview.Escape(message)).
		AddButtons([]string{"Cancel", "Delete"}).
		SetDoneFunc(func(index int, _ string) {
			b.restore()
			if index == 1 {
				b.fileOperation(p, "Delete "+entry.name, func(files ports.SFTPFileSystem) error {
					return files.Remove(context.Background(), target)
				})
			}
		})
	b.app.SetRoot(modal, true)
}

func (b *sftpBrowser) duplicate() {
	p, entry, ok := b.currentSelection()
	if !ok {
		return
	}
	b.promptText("Duplicate "+entry.name, "New name: ", entry.name+" copy", func(name string) error {
		if !validSFTPName(name) || name == entry.name {
			return errors.New("enter a different filename")
		}
		b.restore()
		b.startTransfer(ports.SFTPTransfer{
			Source:      ports.SFTPLocation{Path: p.join(p.dir, entry.name), Remote: p.remote},
			Destination: ports.SFTPLocation{Path: p.join(p.dir, name), Remote: p.remote},
		})
		return nil
	})
}

func (b *sftpBrowser) properties() {
	p, entry, ok := b.currentSelection()
	if !ok {
		return
	}
	target := p.join(p.dir, entry.name)
	b.run("Reading properties…", func() func() {
		info, err := b.session.Files(p.remote).Lstat(target)
		return func() {
			if err != nil {
				b.setStatus(err.Error(), ActiveTheme.Red)
				return
			}
			b.showText("Properties", fmt.Sprintf("Path: %s\n\nType and permissions: %s (%03o)\nSize: %d bytes\nModified: %s",
				displaySFTPText(target), info.Mode(), info.Mode().Perm(), info.Size(),
				info.ModTime().Local().Format(time.RFC3339)))
		}
	})
}

func (b *sftpBrowser) changePermissions() {
	p, entry, ok := b.currentSelection()
	if !ok {
		return
	}
	target := p.join(p.dir, entry.name)
	b.promptText("Permissions for "+entry.name, "Octal: ", fmt.Sprintf("%03o", entry.mode.Perm()), func(value string) error {
		mode, err := strconv.ParseUint(value, 8, 9)
		if err != nil {
			return errors.New("enter octal permissions between 000 and 777")
		}
		b.restore()
		b.fileOperation(p, "Set permissions", func(files ports.SFTPFileSystem) error {
			return files.Chmod(target, fs.FileMode(mode))
		})
		return nil
	})
}

const textPreviewLimit = 256 * 1024

func (b *sftpBrowser) preview() {
	p, entry, ok := b.currentSelection()
	if !ok || !entry.mode.IsRegular() {
		if ok {
			b.setStatus("Select a regular text file", ActiveTheme.Peach)
		}
		return
	}
	target := p.join(p.dir, entry.name)
	b.run("Loading text preview…", func() func() {
		data, err := b.session.Files(p.remote).ReadPreview(target, textPreviewLimit+1)
		title, content := "Preview · "+entry.name, ""
		if err == nil {
			content, err = formatSFTPPreview(data)
		}
		return func() {
			if err != nil {
				b.setStatus(err.Error(), ActiveTheme.Peach)
				return
			}
			b.showText(title, content)
		}
	})
}

func formatSFTPPreview(data []byte) (string, error) {
	truncated := len(data) > textPreviewLimit
	if truncated {
		data = data[:textPreviewLimit]
		// A size boundary can split the final UTF-8 code point.
		for end := len(data) - 1; end >= len(data)-3 && end >= 0; end-- {
			if utf8.RuneStart(data[end]) {
				if !utf8.FullRune(data[end:]) {
					data = data[:end]
				}
				break
			}
		}
	}
	if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
		return "", errors.New("text preview expects a UTF-8 text file")
	}
	content := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\uFEFF")
	content = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '�'
		}
		return r
	}, content)
	if truncated {
		content += "\n\n… Preview limited to the first 256 KiB."
	}
	return content, nil
}

func (b *sftpBrowser) compareDirectories() {
	if b.busy || b.local.dir == "" || b.remote.dir == "" {
		return
	}
	localDir, remoteDir := b.local.dir, b.remote.dir
	b.run("Comparing directory entries…", func() func() {
		_, local, localErr := b.loadDirectory(false, localDir)
		_, remote, remoteErr := b.loadDirectory(true, remoteDir)
		err := errors.Join(localErr, remoteErr)
		report := directoryDifferences(local, remote)
		return func() {
			if err != nil {
				b.setStatus(err.Error(), ActiveTheme.Red)
				return
			}
			b.showText("Directory comparison", report)
		}
	})
}

func directoryDifferences(local, remote []sftpEntry) string {
	l, r := make(map[string]sftpEntry), make(map[string]sftpEntry)
	for _, entry := range local {
		if validSFTPName(entry.name) {
			l[entry.name] = entry
		}
	}
	for _, entry := range remote {
		if validSFTPName(entry.name) {
			r[entry.name] = entry
		}
	}
	var differences []string
	for name, left := range l {
		right, exists := r[name]
		switch {
		case !exists:
			differences = append(differences, "Local only      "+displaySFTPText(name))
		case left.mode.Type() != right.mode.Type() || left.mode.IsRegular() &&
			(left.size != right.size || left.modified.Sub(right.modified).Abs() >= time.Second):
			differences = append(differences, "Different       "+displaySFTPText(name))
		}
	}
	for name := range r {
		if _, exists := l[name]; !exists {
			differences = append(differences, "Remote only     "+displaySFTPText(name))
		}
	}
	slices.Sort(differences)
	if len(differences) == 0 {
		return "No differences in the types, sizes, and modification times of entries in these directories."
	}
	return strings.Join(differences, "\n")
}
