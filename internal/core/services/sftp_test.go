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
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pkg/sftp"
	"github.com/taylorbanks/moshpit/internal/core/ports"
)

func newTestSFTPSession(t *testing.T, remote string) *sftpSession {
	t.Helper()
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() { _ = clientEnd.Close() })
	t.Cleanup(func() { _ = serverEnd.Close() })
	server, err := sftp.NewServer(serverEnd, sftp.WithServerWorkingDirectory(remote))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
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
	t.Cleanup(session.Close)
	return session
}

func transferRequest(source, target string, upload bool) ports.SFTPTransfer {
	return ports.SFTPTransfer{
		Source:      ports.SFTPLocation{Path: source, Remote: !upload},
		Destination: ports.SFTPLocation{Path: target, Remote: upload},
	}
}

func TestSFTPSessionTransfersAndPreservesExistingFiles(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	session := newTestSFTPSession(t, remote)
	const remoteFile = "报告 [1].txt"
	localFile := filepath.Join(local, remoteFile)
	download := filepath.Join(local, "download.txt")
	payload := bytes.Repeat([]byte("example SFTP data\n"), 4096)
	writeSFTPTestFile(t, localFile, payload)

	ctx := context.Background()
	up := transferRequest(localFile, remoteFile, true)
	down := transferRequest(remoteFile, download, false)
	if n, err := session.Transfer(ctx, up, nil); err != nil || n != int64(len(payload)) {
		t.Fatalf("upload returned %d bytes and %v", n, err)
	}
	files, err := session.Files(true).ReadDir(".")
	if err != nil || len(files) != 1 || files[0].Name() != remoteFile {
		t.Fatalf("remote directory = %v, err = %v", files, err)
	}
	if n, err := session.Transfer(ctx, down, nil); err != nil || n != int64(len(payload)) {
		t.Fatalf("download returned %d bytes and %v", n, err)
	}
	assertSFTPTestFile(t, download, payload)

	different := []byte("different contents")
	writeSFTPTestFile(t, localFile, different)
	if _, err := session.Transfer(ctx, up, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("upload to an existing path = %v; want ErrExist", err)
	}
	if _, err := session.Transfer(ctx, transferRequest(remoteFile, localFile, false), nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("download to an existing path = %v; want ErrExist", err)
	}
	assertSFTPTestFile(t, localFile, different)
	assertSFTPTestFile(t, filepath.Join(remote, remoteFile), payload)

	up.Replace = true
	if _, err := session.Transfer(ctx, up, nil); err != nil {
		t.Fatalf("replace explicitly selected regular file: %v", err)
	}
	assertSFTPTestFile(t, filepath.Join(remote, remoteFile), different)
}

func TestSFTPTransfersDirectoryTreesAndEmptyFolders(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	session := newTestSFTPSession(t, remote)
	source := filepath.Join(local, "project")
	if err := os.MkdirAll(filepath.Join(source, "内容", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("nested text file")
	writeSFTPTestFile(t, filepath.Join(source, "内容", ".notes.txt"), payload)

	ctx := context.Background()
	up := transferRequest(source, "copy", true)
	if n, err := session.Transfer(ctx, up, nil); err != nil || n != int64(len(payload)) {
		t.Fatalf("upload tree: %d, %v", n, err)
	}
	if _, err := session.Transfer(ctx, up, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("directory collision: %v", err)
	}
	up.Replace = true
	if _, err := session.Transfer(ctx, up, nil); err == nil {
		t.Fatal("a directory tree must not be replaced by file-transfer overwrite")
	}
	target := filepath.Join(local, "restored")
	if _, err := session.Transfer(ctx, transferRequest("copy", target, false), nil); err != nil {
		t.Fatal(err)
	}
	assertSFTPTestFile(t, filepath.Join(target, "内容", ".notes.txt"), payload)
	empty, err := os.ReadDir(filepath.Join(target, "内容", "empty"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("directory contents = %v, %v; want empty folder", empty, err)
	}
}

func TestSFTPTransferCancellationCleansUpPartialDestination(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	session := newTestSFTPSession(t, remote)
	source := filepath.Join(local, "large.bin")
	writeSFTPTestFile(t, source, bytes.Repeat([]byte{0x55}, 128*1024))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sent int64
	_, err := session.Transfer(ctx, transferRequest(source, "large.bin", true), func(progress ports.SFTPProgress) {
		if progress.Bytes > 0 {
			sent = progress.Bytes
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || sent == 0 {
		t.Fatalf("transfer sent %d bytes and returned %v; want cancellation after progress", sent, err)
	}
	remaining, err := os.ReadDir(remote)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remote files after cancel = %v, %v; want no partial files", remaining, err)
	}
}

func TestSFTPFileOperations(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	session := newTestSFTPSession(t, remote)
	ctx := context.Background()
	for _, side := range []struct {
		files ports.SFTPFileSystem
		at    func(string) string
		root  string
	}{
		{session.Files(false), func(name string) string { return filepath.Join(local, name) }, local},
		{session.Files(true), func(name string) string { return name }, remote},
	} {
		if err := side.files.Mkdir(side.at("folder")); err != nil {
			t.Fatal(err)
		}
		entry := filepath.Join(side.root, "text.txt")
		writeSFTPTestFile(t, entry, []byte("sample content"))
		writeSFTPTestFile(t, filepath.Join(side.root, "existing.txt"), []byte("keep"))
		if err := side.files.Rename(side.at("text.txt"), side.at("existing.txt")); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("rename with target present = %v; want ErrExist", err)
		}
		renamed := side.at("folder/renamed.txt")
		if err := side.files.Rename(side.at("text.txt"), renamed); err != nil {
			t.Fatal(err)
		}
		if err := side.files.Chmod(renamed, 0o640); err != nil {
			t.Fatal(err)
		}
		info, err := side.files.Lstat(renamed)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
			t.Fatalf("chmod did not apply: %v, %v", info, err)
		}
		if contents, err := side.files.ReadPreview(renamed, 6); err != nil || string(contents) != "sample" {
			t.Fatalf("preview result = %q, %v", contents, err)
		}
		if err := side.files.Remove(ctx, side.at("folder")); err != nil {
			t.Fatal(err)
		}
		if _, err := side.files.Lstat(renamed); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("recursive delete did not remove the nested file: %v", err)
		}
		temporary := side.at("blank.txt")
		if err := side.files.CreateEmpty(temporary); err != nil {
			t.Fatal(err)
		}
		empty, err := side.files.Lstat(temporary)
		if err != nil || !empty.Mode().IsRegular() || empty.Size() != 0 {
			t.Fatalf("create empty file returned %v, %v", empty, err)
		}
		if err := side.files.CreateEmpty(side.at("existing.txt")); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("creating empty file would replace an existing file: %v", err)
		}
		if err := side.files.Remove(ctx, temporary); err != nil {
			t.Fatal(err)
		}
		assertSFTPTestFile(t, filepath.Join(side.root, "existing.txt"), []byte("keep"))
	}
}

func writeSFTPTestFile(t *testing.T, filename string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSFTPTestFile(t *testing.T, filename string, want []byte) {
	t.Helper()
	if got, err := os.ReadFile(filename); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s: expected %d bytes, got %d, read error %v", filename, len(want), len(got), err)
	}
}
