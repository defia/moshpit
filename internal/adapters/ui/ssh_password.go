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
	"fmt"

	"github.com/rivo/tview"
)

func (t *tui) handleSSHPassword() {
	server, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	if !t.serverService.PasswordStorageAvailable() {
		t.showStatusTempColor("Saved passwords require a macOS Keychain-enabled build", Hex(ActiveTheme.Red))
		return
	}

	field := tview.NewInputField().SetLabel("SSH password: ").SetMaskCharacter('*').SetFieldWidth(36)
	form := tview.NewForm().AddFormItem(field)
	form.SetBorder(true).SetTitle(" SSH password · " + tview.Escape(server.Alias) + " ")
	status := tview.NewTextView().SetText("Passwords are stored in macOS Keychain for direct SSH.")
	panel := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(status, 2, 0, false).
		AddItem(form, 0, 1, true)

	closeForm := func() {
		field.SetText("")
		t.returnToMain()
	}
	form.AddButton("Save", func() {
		password := []byte(field.GetText())
		field.SetText("")
		err := t.serverService.SaveSSHPassword(server.Alias, password)
		clear(password)
		if err != nil {
			status.SetText(fmt.Sprintf("Could not save password. %v", err))
			t.app.SetFocus(field)
			return
		}
		closeForm()
		t.showStatusTemp("SSH password saved in macOS Keychain")
	})
	form.AddButton("Delete saved password", func() {
		if err := t.serverService.DeleteSSHPassword(server.Alias); err != nil {
			status.SetText(fmt.Sprintf("Could not delete password. %v", err))
			return
		}
		closeForm()
		t.showStatusTemp("Saved SSH password removed")
	})
	form.AddButton("Connect manually", func() {
		closeForm()
		var err error
		t.app.Suspend(func() {
			err = t.serverService.SSHManual(server.Alias)
		})
		t.refreshServerList()
		if err != nil {
			t.showStatusTempColor("Manual SSH connection ended with an error", Hex(ActiveTheme.Red))
		}
	})
	form.AddButton("Cancel", closeForm)
	form.SetCancelFunc(closeForm)
	t.app.SetRoot(panel, true)
}
