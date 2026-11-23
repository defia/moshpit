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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type sftpSession struct {
	client *sftp.Client
	stop   func()
	close  sync.Once
}

func sftpSSHOptions() []string {
	return []string{
		"-T", "-s",
		"-o", "RemoteCommand=none",
		"-o", "ClearAllForwardings=yes",
		"-o", "PermitLocalCommand=no",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "StdinNull=no",
		"-o", "ForkAfterAuthentication=no",
	}
}

func (s *serverService) OpenSFTP(alias string, manual bool) (ports.SFTPSession, error) {
	options := sftpSSHOptions()
	if !manual {
		cmd, done, err := s.connectionWithSavedPassword(alias, options, "sftp")
		if err != nil {
			s.logger.Warnw("saved password unavailable for SFTP, using manual authentication", "alias", alias, "error", err)
		} else if cmd != nil {
			defer done()
			return startSFTP(cmd)
		}
	}

	// #nosec G204 -- the fixed subsystem is requested from the selected alias.
	cmd := exec.Command("ssh", append(options, "--", alias, "sftp")...)
	return startSFTP(cmd)
}

// Only print SSH diagnostics while the terminal is suspended for authentication.
type sftpDiagnostics struct {
	mu    sync.Mutex
	quiet bool
	text  bytes.Buffer
}

func (d *sftpDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.quiet {
		return len(p), nil
	}
	const limit = 4096
	if remaining := limit - d.text.Len(); remaining > 0 {
		d.text.Write(p[:min(len(p), remaining)])
	}
	return os.Stderr.Write(p)
}

func (d *sftpDiagnostics) finish() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.quiet = true
	message := strings.Join(strings.Fields(d.text.String()), " ")
	d.text.Reset()
	return message
}

func startSFTP(cmd *exec.Cmd) (*sftpSession, error) {
	// Own these pipes so reaping a disconnected SSH process does not close
	// the response reader before the SFTP client reads the buffered data.
	requestReader, requestWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer requestReader.Close()
	responseReader, responseWriter, err := os.Pipe()
	if err != nil {
		_ = requestWriter.Close()
		return nil, err
	}
	defer responseWriter.Close()

	diagnostics := &sftpDiagnostics{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = requestReader, responseWriter, diagnostics
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		_ = requestWriter.Close()
		_ = responseReader.Close()
		return nil, fmt.Errorf("could not start OpenSSH: %w", err)
	}
	_ = requestReader.Close()
	_ = responseWriter.Close()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	stop := func() {
		_ = cmd.Process.Kill()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		<-done
	}

	client, err := sftp.NewClientPipe(responseReader, requestWriter, sftp.UseConcurrentWrites(true))
	if err != nil {
		stop()
		if detail := diagnostics.finish(); detail != "" {
			return nil, fmt.Errorf("could not open the SFTP subsystem: %s: %w", detail, err)
		}
		return nil, fmt.Errorf("could not open the SFTP subsystem: %w", err)
	}
	diagnostics.finish()
	return &sftpSession{client: client, stop: stop}, nil
}

func (s *sftpSession) Close() {
	s.close.Do(func() {
		s.stop()
		_ = s.client.Close()
	})
}

func (s *sftpSession) Getwd() (string, error) { return s.client.Getwd() }
