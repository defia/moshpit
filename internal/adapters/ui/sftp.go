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
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

func (t *tui) handleSFTP() {
	if server, ok := t.serverList.GetSelectedServer(); ok {
		t.openSFTP(server.Alias, false)
	}
}

func (t *tui) openSFTP(alias string, manual bool) {
	var session ports.SFTPSession
	var err error
	if !t.app.Suspend(func() {
		session, err = t.serverService.OpenSFTP(alias, manual)
	}) {
		t.showStatusTempColor("Could not open the terminal for SFTP authentication", Hex(ActiveTheme.Red))
		return
	}
	if err != nil {
		modal := tview.NewModal().SetText("SFTP connection failed.\n" + tview.Escape(displaySFTPText(err.Error()))).
			AddButtons([]string{"Close"})
		if !manual {
			modal.AddButtons([]string{"Retry manually"})
		}
		modal.SetDoneFunc(func(index int, _ string) {
			if index == 1 {
				t.openSFTP(alias, true)
				return
			}
			t.returnToMain()
		})
		t.app.SetRoot(modal, true)
		return
	}
	browser := newSFTPBrowser(t.app, session, alias, t.returnToMain)
	t.app.SetRoot(browser.root, true).SetFocus(browser.local.list)
	browser.refresh()
}

type sftpEntry struct {
	name string
	mode fs.FileMode
	size int64
}

type sftpPanel struct {
	list    *tview.List
	label   string
	dir     string
	entries []sftpEntry
	remote  bool
}

func newSFTPPanel(label string, remote bool) *sftpPanel {
	list := tview.NewList().ShowSecondaryText(false).SetUseStyleTags(false, false).
		SetSelectedFocusOnly(true).SetHighlightFullLine(true).
		SetSelectedTextColor(ActiveTheme.Base).SetSelectedBackgroundColor(ActiveTheme.Blue)
	list.SetBorder(true).SetTitleAlign(tview.AlignLeft).SetTitle(" " + label + " ")
	return &sftpPanel{list: list, label: label, remote: remote}
}

func (p *sftpPanel) join(dir, name string) string {
	if p.remote {
		return path.Join(dir, name)
	}
	return filepath.Join(dir, name)
}

func (p *sftpPanel) selected() (sftpEntry, bool) {
	i := p.list.GetCurrentItem()
	if i < 0 || i >= len(p.entries) {
		return sftpEntry{}, false
	}
	return p.entries[i], true
}

func (p *sftpPanel) update(dir string, entries []sftpEntry) {
	selected, keepSelection := p.selected()
	keepSelection = keepSelection && dir == p.dir
	p.dir, p.entries = dir, entries
	p.list.Clear()
	p.list.SetTitle(" " + p.label + " · " + tview.Escape(displaySFTPText(dir)) + " ")
	selectedIndex := 0
	for i, entry := range entries {
		label := displaySFTPText(entry.name)
		switch {
		case entry.mode.IsDir():
			label += "/"
		case entry.mode.IsRegular():
			label += "  ·  " + sftpSize(entry.size)
		default:
			label += "  ·  " + entry.mode.Type().String()
		}
		p.list.AddItem(label, "", 0, nil)
		if keepSelection && entry.name == selected.name {
			selectedIndex = i
		}
	}
	p.list.SetCurrentItem(selectedIndex)
}

type sftpBrowser struct {
	app     *tview.Application
	session ports.SFTPSession
	root    *tview.Flex
	local   *sftpPanel
	remote  *sftpPanel
	status  *tview.TextView
	done    chan struct{}
	closed  bool
	busy    bool
	onClose func()
}

func newSFTPBrowser(app *tview.Application, session ports.SFTPSession, alias string, onClose func()) *sftpBrowser {
	b := &sftpBrowser{
		app: app, session: session, onClose: onClose, done: make(chan struct{}),
		local: newSFTPPanel("Local", false), remote: newSFTPPanel("Remote", true),
		status: tview.NewTextView().SetWrap(false),
	}
	b.local.dir, _ = os.Getwd()
	if b.local.dir == "" {
		b.local.dir = "."
	}
	for _, panel := range []*sftpPanel{b.local, b.remote} {
		panel.list.SetSelectedFunc(func(index int, _, _ string, _ rune) {
			if index >= 0 && index < len(panel.entries) && panel.entries[index].mode.IsDir() {
				b.openDirectory(panel, panel.join(panel.dir, panel.entries[index].name))
			}
		})
	}
	panels := tview.NewFlex().AddItem(b.local.list, 0, 1, true).AddItem(b.remote.list, 0, 1, false)
	buttons := tview.NewFlex().
		AddItem(tview.NewButton("u  Upload →").SetSelectedFunc(func() { b.transfer(b.local, b.remote) }), 0, 1, false).
		AddItem(tview.NewButton("d  ← Download").SetSelectedFunc(func() { b.transfer(b.remote, b.local) }), 0, 1, false).
		AddItem(tview.NewButton("r  Refresh").SetSelectedFunc(b.refresh), 0, 1, false).
		AddItem(tview.NewButton("Esc  Close").SetSelectedFunc(b.close), 0, 1, false)
	header := tview.NewTextView().SetText(" SFTP · " + displaySFTPText(alias)).
		SetTextColor(ActiveTheme.Blue)
	b.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(panels, 0, 1, true).
		AddItem(buttons, 1, 0, false).
		AddItem(b.status, 1, 0, false)
	b.root.SetInputCapture(b.handleKey)
	return b
}

func (b *sftpBrowser) handleKey(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape:
		b.close()
		return nil
	case tcell.KeyTab, tcell.KeyBacktab:
		if b.app.GetFocus() == b.local.list {
			b.app.SetFocus(b.remote.list)
		} else {
			b.app.SetFocus(b.local.list)
		}
		return nil
	case tcell.KeyLeft:
		b.app.SetFocus(b.local.list)
		return nil
	case tcell.KeyRight:
		b.app.SetFocus(b.remote.list)
		return nil
	}
	switch event.Rune() {
	case 'u':
		b.transfer(b.local, b.remote)
	case 'd':
		b.transfer(b.remote, b.local)
	case 'r':
		b.refresh()
	default:
		return event
	}
	return nil
}

func (b *sftpBrowser) close() {
	if b.closed {
		return
	}
	b.closed = true
	close(b.done)
	go b.session.Close()
	b.onClose()
}

func (b *sftpBrowser) run(message string, work func() func()) {
	if b.busy {
		return
	}
	b.busy = true
	b.setStatus(message, ActiveTheme.Text)
	go func() {
		finish := work()
		select {
		case <-b.done:
			return
		default:
		}
		b.app.QueueUpdateDraw(func() {
			if !b.closed {
				b.busy = false
				finish()
			}
		})
	}()
}

func (b *sftpBrowser) setStatus(message string, color tcell.Color) {
	b.status.SetTextColor(color).SetText(" " + displaySFTPText(message))
}

func (b *sftpBrowser) loadDirectory(remote bool, dir string) (string, []sftpEntry, error) {
	var entries []sftpEntry
	if remote {
		if dir == "" {
			var err error
			dir, err = b.session.Getwd()
			if err != nil {
				return "", nil, err
			}
		}
		files, err := b.session.ReadDir(dir)
		if err != nil {
			return dir, nil, err
		}
		for _, info := range files {
			if validSFTPName(info.Name()) {
				entries = append(entries, sftpEntry{info.Name(), info.Mode(), info.Size()})
			}
		}
	} else {
		files, err := os.ReadDir(dir)
		if err != nil {
			return dir, nil, err
		}
		for _, file := range files {
			info, err := file.Info()
			if err == nil && validSFTPName(file.Name()) {
				entries = append(entries, sftpEntry{file.Name(), info.Mode(), info.Size()})
			}
		}
	}
	slices.SortFunc(entries, compareSFTPEntries)
	parent := filepath.Dir(dir)
	if remote {
		parent = path.Dir(dir)
	}
	if parent != dir {
		entries = append([]sftpEntry{{name: "..", mode: fs.ModeDir}}, entries...)
	}
	return dir, entries, nil
}

func (b *sftpBrowser) refresh() {
	localDir, remoteDir := b.local.dir, b.remote.dir
	b.run("Reading directories…", func() func() {
		localDir, local, localErr := b.loadDirectory(false, localDir)
		remoteDir, remote, remoteErr := b.loadDirectory(true, remoteDir)
		return func() {
			if localErr == nil {
				b.local.update(localDir, local)
			}
			if remoteErr == nil {
				b.remote.update(remoteDir, remote)
			}
			switch {
			case localErr != nil:
				b.setStatus("Local directory: "+localErr.Error(), ActiveTheme.Red)
			case remoteErr != nil:
				b.setStatus("Remote directory: "+remoteErr.Error(), ActiveTheme.Red)
			default:
				b.setStatus("Tab or ←/→ switches panes · Enter or click opens a directory", ActiveTheme.Subtext0)
			}
		}
	})
}

func (b *sftpBrowser) openDirectory(panel *sftpPanel, dir string) {
	b.run("Reading "+panel.label+" directory…", func() func() {
		dir, entries, err := b.loadDirectory(panel.remote, dir)
		return func() {
			if err != nil {
				b.setStatus(err.Error(), ActiveTheme.Red)
				return
			}
			panel.update(dir, entries)
			b.setStatus("", ActiveTheme.Text)
		}
	})
}

func (b *sftpBrowser) transfer(from, to *sftpPanel) {
	if b.busy {
		return
	}
	entry, selected := from.selected()
	if !selected || !entry.mode.IsRegular() || !validSFTPName(entry.name) {
		b.setStatus("Select a regular file in the "+from.label+" pane", ActiveTheme.Peach)
		return
	}
	if to.dir == "" {
		b.setStatus("Open a destination directory first", ActiveTheme.Peach)
		return
	}
	source, dest := from.join(from.dir, entry.name), to.join(to.dir, entry.name)
	destinationDir := to.dir
	verb, finished := "Uploading", "Uploaded"
	transfer := b.session.Upload
	if from.remote {
		verb, finished = "Downloading", "Downloaded"
		transfer = b.session.Download
	}
	b.run(verb+" "+entry.name+"…", func() func() {
		bytes, err := transfer(source, dest)
		if err != nil {
			return func() {
				if errors.Is(err, fs.ErrExist) {
					b.setStatus("Destination already contains "+entry.name, ActiveTheme.Peach)
				} else {
					b.setStatus(verb+" failed: "+err.Error(), ActiveTheme.Red)
				}
			}
		}
		dir, entries, err := b.loadDirectory(to.remote, destinationDir)
		return func() {
			if err == nil {
				to.update(dir, entries)
			}
			message := fmt.Sprintf("%s %s (%s)", finished, entry.name, sftpSize(bytes))
			if err != nil {
				b.setStatus(message+" · Refresh failed: "+err.Error(), ActiveTheme.Peach)
				return
			}
			b.setStatus(message, ActiveTheme.Green)
		}
	})
}

func compareSFTPEntries(a, b sftpEntry) int {
	if a.mode.IsDir() != b.mode.IsDir() {
		if a.mode.IsDir() {
			return -1
		}
		return 1
	}
	if order := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); order != 0 {
		return order
	}
	return strings.Compare(a.name, b.name)
}

func validSFTPName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\x00") && filepath.Base(name) == name && filepath.IsLocal(name)
}

func displaySFTPText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, text)
}

func sftpSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	size := float64(n)
	for _, scale := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		size /= unit
		if size < unit || scale == "EiB" {
			return fmt.Sprintf("%.1f %s", size, scale)
		}
	}
	return fmt.Sprintf("%d B", n)
}
