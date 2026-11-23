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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"time"

	"github.com/rivo/tview"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

func (b *sftpBrowser) transfer(from, to *sftpPanel) {
	if b.busy {
		return
	}
	entry, selected := from.selected()
	if !selected || !validSFTPName(entry.name) || !(entry.mode.IsRegular() || entry.mode.IsDir()) {
		b.setStatus("Select a file or directory in "+from.label, ActiveTheme.Peach)
		return
	}
	if to.dir == "" {
		b.setStatus("Open a destination directory first", ActiveTheme.Peach)
		return
	}
	b.startTransfer(ports.SFTPTransfer{
		Source:      ports.SFTPLocation{Path: from.join(from.dir, entry.name), Remote: from.remote},
		Destination: ports.SFTPLocation{Path: to.join(to.dir, entry.name), Remote: to.remote},
	})
}

func sftpLocationParts(location ports.SFTPLocation) (dir, name string) {
	if location.Remote {
		return path.Dir(location.Path), path.Base(location.Path)
	}
	return filepath.Dir(location.Path), filepath.Base(location.Path)
}

func (b *sftpBrowser) startTransfer(request ports.SFTPTransfer) {
	if b.busy || b.closed {
		return
	}
	destinationDir, name := sftpLocationParts(request.Destination)
	to := b.local
	verb, completed := "Downloading", "Downloaded"
	if request.Destination.Remote {
		to = b.remote
		verb, completed = "Uploading", "Uploaded"
	}
	if request.Source.Remote == request.Destination.Remote {
		verb, completed = "Copying", "Copied"
	}
	currentDir := to.dir
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelTransfer = cancel
	b.cancelling = false
	b.lastTransfer = &request
	b.run(verb+" "+name+"…", func() func() {
		defer cancel()
		var lastReport time.Time
		bytes, err := b.session.Transfer(ctx, request, func(progress ports.SFTPProgress) {
			if ctx.Err() != nil || time.Since(lastReport) < 125*time.Millisecond {
				return
			}
			lastReport = time.Now()
			if isClosed(b.done) {
				return
			}
			_, current := sftpLocationParts(ports.SFTPLocation{Path: progress.Path, Remote: request.Source.Remote})
			message := fmt.Sprintf("%s %s  %s / %s  ·  %s total  ·  %d files",
				verb, current, sftpSize(progress.FileBytes), sftpSize(progress.FileSize),
				sftpSize(progress.Bytes), progress.Files)
			b.app.QueueUpdateDraw(func() {
				if !b.closed && ctx.Err() == nil {
					b.setStatus(message, ActiveTheme.Text)
				}
			})
		})
		var replaceAllowed bool
		if errors.Is(err, fs.ErrExist) && !request.Replace {
			src, srcErr := b.session.Files(request.Source.Remote).Lstat(request.Source.Path)
			dest, destErr := b.session.Files(request.Destination.Remote).Lstat(request.Destination.Path)
			replaceAllowed = srcErr == nil && destErr == nil && src.Mode().IsRegular() && dest.Mode().IsRegular()
		}
		var entries []sftpEntry
		var refreshErr error
		if err == nil && currentDir == destinationDir {
			_, entries, refreshErr = b.loadDirectory(to.remote, currentDir)
		}
		return func() {
			b.cancelTransfer = nil
			b.cancelling = false
			switch {
			case errors.Is(err, fs.ErrExist) && !request.Replace:
				b.transferConflict(request, replaceAllowed)
			case errors.Is(err, context.Canceled):
				message := "Transfer cancelled"
				if err.Error() != context.Canceled.Error() {
					message += " · " + err.Error()
				}
				b.setStatus(message, ActiveTheme.Peach)
			case err != nil:
				b.setStatus(verb+" failed: "+err.Error(), ActiveTheme.Red)
			default:
				b.lastTransfer = nil
				message := fmt.Sprintf("%s %s (%s)", completed, name, sftpSize(bytes))
				if refreshErr != nil {
					b.setStatus(message+" · Refresh failed: "+refreshErr.Error(), ActiveTheme.Peach)
					return
				}
				if currentDir == destinationDir {
					to.update(currentDir, entries)
				}
				b.setStatus(message, ActiveTheme.Green)
			}
		}
	})
}

func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (b *sftpBrowser) transferConflict(request ports.SFTPTransfer, replaceAllowed bool) {
	title := "Destination already exists"
	message := title + "\n\n" + displaySFTPText(request.Destination.Path)
	buttons := []string{"Keep existing", "Save as…"}
	if replaceAllowed {
		buttons = append(buttons, "Replace file")
	}
	modal := tview.NewModal().SetText(tview.Escape(message)).AddButtons(buttons).
		SetDoneFunc(func(index int, _ string) {
			b.restore()
			switch index {
			case 1:
				b.transferWithNewName(request)
			case 2:
				b.lastTransfer = nil
				request.Replace = true
				b.startTransfer(request)
			default:
				b.lastTransfer = nil
				b.setStatus("Existing destination preserved", ActiveTheme.Peach)
			}
		})
	b.app.SetRoot(modal, true)
}

func (b *sftpBrowser) transferWithNewName(request ports.SFTPTransfer) {
	dir, name := sftpLocationParts(request.Destination)
	b.promptText("Save transferred copy", "Name: ", name+" copy", func(value string) error {
		if !validSFTPName(value) || value == name {
			return errors.New("enter a different filename")
		}
		to := b.local
		if request.Destination.Remote {
			to = b.remote
		}
		request.Destination.Path = to.join(dir, value)
		request.Replace = false
		b.restore()
		b.startTransfer(request)
		return nil
	})
}

func (b *sftpBrowser) backOrCancel() {
	if b.cancelTransfer != nil && !b.cancelling {
		b.cancelling = true
		b.cancelTransfer()
		b.setStatus("Cancelling transfer… · Esc again closes the connection", ActiveTheme.Peach)
		return
	}
	b.close()
}
