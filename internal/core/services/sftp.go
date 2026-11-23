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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

	client, err := sftp.NewClientPipe(responseReader, requestWriter)
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

func (s *sftpSession) ReadDir(directory string) ([]fs.FileInfo, error) {
	return s.client.ReadDir(directory)
}

func (s *sftpSession) Upload(localFile, remoteFile string) (int64, error) {
	// Refuse a destination collision before sending the contents.
	if _, err := s.client.Lstat(remoteFile); err == nil {
		return 0, fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	info, err := os.Lstat(localFile)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("select a regular local file")
	}
	// #nosec G304 -- the source is chosen by the user in the local file panel.
	source, err := os.Open(localFile)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil {
		return 0, err
	}
	if !os.SameFile(info, opened) {
		return 0, errors.New("the source file changed before it could be opened")
	}

	var nonce [16]byte
	rand.Read(nonce[:])
	tempName := path.Join(path.Dir(remoteFile), ".moshpit-transfer-"+hex.EncodeToString(nonce[:]))
	target, err := s.client.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(target, source)
	if err := errors.Join(copyErr, target.Close()); err != nil {
		_ = s.client.Remove(tempName)
		return n, err
	}
	// SFTP v3 Rename fails if the destination exists. PosixRename overwrites it.
	if err := s.client.Rename(tempName, remoteFile); err != nil {
		_ = s.client.Remove(tempName)
		return n, err
	}
	return n, nil
}

func (s *sftpSession) Download(remoteFile, localFile string) (int64, error) {
	if _, err := os.Lstat(localFile); err == nil {
		return 0, fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	info, err := s.client.Lstat(remoteFile)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("select a regular remote file")
	}
	source, err := s.client.Open(remoteFile)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	target, err := os.CreateTemp(filepath.Dir(localFile), ".moshpit-transfer-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(target.Name())
	// Use the file's WriterTo implementation for streaming SFTP reads.
	n, copyErr := io.Copy(target, source)
	if err := errors.Join(copyErr, target.Close()); err != nil {
		return n, err
	}
	// Publishing a hard link makes the completed file visible without an overwrite race.
	return n, os.Link(target.Name(), localFile)
}
