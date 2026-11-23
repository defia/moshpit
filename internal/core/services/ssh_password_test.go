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
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAskpassReleasesPasswordOnlyForSSHPasswordPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("The Keychain askpass helper uses a Unix socket")
	}
	tests := []struct {
		name     string
		prompt   string
		wrongKey bool
		want     string
	}{
		{name: "effective user and host", prompt: "user@example.com@bastion's password: ", want: "synthetic test value"},
		{name: "other username", prompt: "other@bastion's password: "},
		{name: "other host", prompt: "user@example.com@other's password: "},
		{name: "key passphrase", prompt: "Enter passphrase for key '/tmp/key': "},
		{name: "keyboard interactive", prompt: "Password: "},
		{name: "password change", prompt: "Enter user@example.com@bastion's new password: "},
		{name: "invalid token", prompt: "user@example.com@bastion's password: ", wrongKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("ssh")
			target := passwordTarget{host: "bastion.example.com", user: "user@example.com", hostKeyAlias: "bastion"}
			var reads atomic.Int32
			cleanup, err := attachAskpass(cmd, target, func() ([]byte, error) {
				reads.Add(1)
				return []byte("synthetic test value"), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()

			env := make(map[string]string)
			for _, value := range cmd.Env {
				key, value, _ := strings.Cut(value, "=")
				env[key] = value
			}
			token := env[askpassToken]
			if tt.wrongKey {
				token = "invalid"
			}
			conn, err := net.Dial("unix", env[askpassSocket])
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(conn).Encode(askpassRequest{Token: token, Prompt: tt.prompt}); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(conn)
			if err != nil || string(got) != tt.want {
				t.Fatalf("askpass response length = %d, want %d, error = %v", len(got), len(tt.want), err)
			}
			if expectedRead := tt.want != ""; (reads.Load() == 1) != expectedRead {
				t.Fatalf("credential reads = %d, expected a read: %v", reads.Load(), expectedRead)
			}
		})
	}
}

func TestPasswordTargetUsesEffectiveSSHIdentity(t *testing.T) {
	config := "hostname 2001:db8::1\nport 2222\nuser user@example.com\n"
	target, err := parsePasswordTarget(config)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := target.binding(), "[2001:db8::1]:2222\nuser@example.com\n"; got != want {
		t.Errorf("binding = %q, want %q", got, want)
	}
	for _, setting := range []string{"batchmode yes", "passwordauthentication no", "proxyjump bastion", "proxycommand ssh bastion", "knownhostscommand helper"} {
		if _, err := parsePasswordTarget(config + setting + "\n"); err == nil {
			t.Errorf("unexpected eligibility with %s", setting)
		}
	}
}
