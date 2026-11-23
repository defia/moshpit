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
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const askpassSocket = "MOSHPIT_ASKPASS_SOCKET"
const askpassToken = "MOSHPIT_ASKPASS_TOKEN"

type askpassRequest struct {
	Token  string
	Prompt string
}

// RunAskpass handles OpenSSH's helper invocation before the TUI and logger start.
func RunAskpass() (handled bool, exitCode int) {
	socket := os.Getenv(askpassSocket)
	if socket == "" {
		return false, 0
	}
	if len(os.Args) != 2 || os.Getenv("SSH_ASKPASS_PROMPT") != "" {
		return true, 1
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return true, 1
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(askpassRequest{os.Getenv(askpassToken), os.Args[1]}); err != nil {
		return true, 1
	}
	// This stdout is the private pipe OpenSSH designated for its askpass response.
	written, err := io.Copy(os.Stdout, io.LimitReader(conn, maxSSHPasswordBytes))
	if err != nil || written == 0 {
		return true, 1
	}
	return true, 0
}

func attachAskpass(cmd *exec.Cmd, target passwordTarget, readPassword func() ([]byte, error)) (func(), error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// Use a short path to fit the sockaddr_un limit on macOS.
	dir, err := os.MkdirTemp("/tmp", "moshpit-askpass-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "askpass")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	cleanup := func() {
		_ = listener.Close()
		_ = os.Remove(dir)
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		cleanup()
		return nil, err
	}
	token := hex.EncodeToString(entropy[:])
	cmd.Env = append(cmd.Environ(),
		"SSH_ASKPASS="+executable, "SSH_ASKPASS_REQUIRE=force", "SSH_ASKPASS_PROMPT=",
		askpassSocket+"="+socket, askpassToken+"="+token,
	)
	go func() {
		defer cleanup()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var req askpassRequest
		if err := json.NewDecoder(io.LimitReader(conn, 8192)).Decode(&req); err != nil {
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 || !target.matchesPrompt(req.Prompt) {
			return
		}
		password, err := readPassword()
		defer clear(password)
		if err != nil || !validSSHPassword(password) {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write(password)
	}()
	return cleanup, nil
}
