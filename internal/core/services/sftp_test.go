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
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

func TestSFTPSessionTransfersAndPreservesExistingFiles(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close()
	defer serverEnd.Close()
	server, err := sftp.NewServer(serverEnd, sftp.WithServerWorkingDirectory(remote))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		_ = server.Serve()
		close(done)
	}()

	client, err := sftp.NewClientPipe(clientEnd, clientEnd)
	if err != nil {
		t.Fatal(err)
	}
	session := &sftpSession{
		client: client,
		stop: func() {
			_ = clientEnd.Close()
			<-done
		},
	}
	defer session.Close()

	const remoteFile = "报告 [1].txt"
	localFile := filepath.Join(local, remoteFile)
	download := filepath.Join(local, "download.txt")
	payload := bytes.Repeat([]byte("example SFTP data\n"), 4096)
	if err := os.WriteFile(localFile, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if n, err := session.Upload(localFile, remoteFile); err != nil || n != int64(len(payload)) {
		t.Fatalf("upload returned %d bytes and %v", n, err)
	}
	files, err := session.ReadDir(".")
	if err != nil || len(files) != 1 || files[0].Name() != remoteFile {
		t.Fatalf("remote directory = %v, err = %v", files, err)
	}
	if n, err := session.Download(remoteFile, download); err != nil || n != int64(len(payload)) {
		t.Fatalf("download returned %d bytes and %v", n, err)
	}
	if got, err := os.ReadFile(download); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes did not match, err = %v", err)
	}

	if err := os.WriteFile(localFile, []byte("different contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Upload(localFile, remoteFile); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("upload to an existing path = %v; want ErrExist", err)
	}
	if _, err := session.Download(remoteFile, localFile); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("download to an existing path = %v; want ErrExist", err)
	}
	if got, err := os.ReadFile(localFile); err != nil || string(got) != "different contents" {
		t.Fatalf("existing local file changed, err = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(remote, remoteFile)); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("existing remote file changed, err = %v", err)
	}
}
