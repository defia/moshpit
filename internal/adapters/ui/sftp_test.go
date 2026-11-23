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
	"io/fs"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestSFTPBrowserShowsPanelLabelsWithLongPaths(t *testing.T) {
	browser := newSFTPBrowser(tview.NewApplication(), nil, "demo", nil)
	deepPath := "/" + strings.Repeat("long-directory/", 6)
	browser.local.update(deepPath, []sftpEntry{{name: "local file.txt"}})
	browser.remote.update(deepPath, []sftpEntry{{name: "remote file.txt"}})

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 20)
	browser.root.SetRect(0, 0, 80, 20)
	browser.root.Draw(screen)
	screen.Show()
	cells, _, _ := screen.GetContents()
	var rendered strings.Builder
	for _, cell := range cells {
		rendered.WriteString(string(cell.Runes))
	}
	for _, label := range []string{"Local ·", "Remote ·", "local file.txt", "remote file.txt", "Upload", "Download"} {
		if !strings.Contains(rendered.String(), label) {
			t.Errorf("browser did not display %q", label)
		}
	}
}

func TestSFTPTextPreviewBoundsAndSanitizesContent(t *testing.T) {
	got, err := formatSFTPPreview([]byte("\uFEFF中文\r\nsecond\tcolumn\x1b[0m"))
	if err != nil || got != "中文\nsecond\tcolumn�[0m" {
		t.Fatalf("preview = %q, %v", got, err)
	}
	if _, err := formatSFTPPreview([]byte{0x00, 'x'}); err == nil {
		t.Fatal("binary input should not be previewed")
	}
	long := strings.Repeat("a", textPreviewLimit-1) + "中"
	got, err = formatSFTPPreview([]byte(long))
	if err != nil || !strings.Contains(got, "Preview limited to the first 256 KiB") ||
		strings.ContainsRune(got, '中') {
		t.Fatalf("preview at a split UTF-8 boundary did not truncate safely: %v", err)
	}
}

func TestSFTPDeleteActionKeepsRemoteDirectorySelected(t *testing.T) {
	for _, localSelection := range []sftpEntry{
		{name: "..", mode: fs.ModeDir},
		{name: "local-file.txt"},
	} {
		t.Run(localSelection.name, func(t *testing.T) {
			app := tview.NewApplication()
			browser := newSFTPBrowser(app, nil, "test host", nil)
			browser.local.update("/local", []sftpEntry{localSelection})
			browser.remote.update("/remote", []sftpEntry{{name: "delete-directory", mode: fs.ModeDir}})
			app.SetRoot(browser.root, true).SetFocus(browser.remote.list)

			browser.handleKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
			menu, ok := app.GetFocus().(*tview.List)
			if !ok || menu == browser.local.list || menu == browser.remote.list {
				t.Fatal("SFTP actions menu did not open")
			}
			for index := 0; index < menu.GetItemCount(); index++ {
				label, _ := menu.GetItemText(index)
				if !strings.HasPrefix(label, "Delete ") {
					continue
				}
				menu.SetCurrentItem(index)
				menu.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) {
					app.SetFocus(p)
				})
				if browser.active != browser.remote {
					t.Fatalf("Delete switched the selected pane to %s; status = %q",
						browser.active.label, strings.TrimSpace(browser.status.GetText(false)))
				}
				cancel, ok := app.GetFocus().(*tview.Button)
				if !ok || cancel.GetLabel() != "Cancel" {
					t.Fatal("remote directory deletion did not open a confirmation with Cancel focused")
				}
				cancel.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) {
					app.SetFocus(p)
				})
				if app.GetFocus() != browser.remote.list || browser.active != browser.remote {
					t.Fatal("cancel did not return focus to the remote pane")
				}
				return
			}
			t.Fatal("SFTP actions menu is missing Delete")
		})
	}
}
