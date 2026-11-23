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
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/taylorbanks/moshpit/internal/core/ports"
)

const sftpStagingPrefix = ".moshpit-transfer-"

func (s *sftpSession) Transfer(ctx context.Context, request ports.SFTPTransfer, report func(ports.SFTPProgress)) (int64, error) {
	from, to := s.files(request.Source.Remote), s.files(request.Destination.Remote)
	source, target := request.Source.Path, request.Destination.Path
	if source == "" || target == "" ||
		request.Source.Remote == request.Destination.Remote && source == target {
		return 0, errors.New("select different source and destination paths")
	}
	info, err := from.Lstat(source)
	if err != nil {
		return 0, err
	}
	if err := checkTransferDestination(to, target, info, request.Replace); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	var random [16]byte
	rand.Read(random[:])
	staging := to.join(to.dir(target), sftpStagingPrefix+hex.EncodeToString(random[:]))
	transfer := &treeTransfer{ctx: ctx, from: from, to: to, report: report, buffer: make([]byte, 256*1024)}
	err = transfer.copy(source, staging, info, 0)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = checkTransferDestination(to, target, info, request.Replace)
	}
	if err == nil {
		err = to.rename(staging, target, request.Replace)
	}
	if err != nil && transfer.staged {
		cleanupErr := to.Remove(context.Background(), staging)
		if cleanupErr != nil && !errors.Is(cleanupErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("could not remove partial transfer %s: %w", staging, cleanupErr))
		}
	}
	return transfer.progress.Bytes, err
}

func checkTransferDestination(to browserFileSystem, target string, source fs.FileInfo, replace bool) error {
	info, err := to.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !replace:
		return fs.ErrExist
	case !info.Mode().IsRegular() || !source.Mode().IsRegular():
		return errors.New("replacement is available only when both items are regular files")
	default:
		return nil
	}
}

type treeTransfer struct {
	ctx      context.Context
	from, to browserFileSystem
	report   func(ports.SFTPProgress)
	progress ports.SFTPProgress
	staged   bool
	buffer   []byte
}

func (t *treeTransfer) copy(source, target string, info fs.FileInfo, depth int) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return errors.New("directory exceeds the supported nesting depth")
	}
	switch {
	case info.IsDir():
		return t.copyDirectory(source, target, info, depth)
	case info.Mode().IsRegular():
		return t.copyFile(source, target, info)
	default:
		return fmt.Errorf("%s is a link or a special file", source)
	}
}

func (t *treeTransfer) copyDirectory(source, target string, info fs.FileInfo, depth int) error {
	entries, err := t.from.ReadDir(source)
	if err != nil {
		return err
	}
	if err := t.to.Mkdir(target); err != nil {
		return err
	}
	t.staged = true
	for _, entry := range entries {
		name := entry.Name()
		if !safeBrowserName(name) {
			return fmt.Errorf("directory contains an unsupported filename: %q", name)
		}
		child := t.from.join(source, name)
		info, err := t.from.Lstat(child)
		if err != nil {
			return err
		}
		if err := t.copy(child, t.to.join(target, name), info, depth+1); err != nil {
			return err
		}
	}
	_ = t.to.chtimes(target, info.ModTime())
	return nil
}

func (t *treeTransfer) copyFile(source, target string, info fs.FileInfo) error {
	in, err := t.from.openRegular(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := t.to.create(target)
	if err != nil {
		return err
	}
	t.staged = true
	t.progress.Path, t.progress.FileBytes, t.progress.FileSize = source, 0, info.Size()
	if t.report != nil {
		t.report(t.progress)
	}
	_, copyErr := io.CopyBuffer(&transferWriter{transfer: t, writer: out}, transferReader{ctx: t.ctx, reader: in}, t.buffer)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if t.progress.FileBytes != info.Size() {
		return fmt.Errorf("%s changed size while it was being transferred", source)
	}
	t.progress.Files++
	_ = t.to.chtimes(target, info.ModTime())
	return nil
}

type transferReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type transferWriter struct {
	transfer *treeTransfer
	writer   io.Writer
}

func (w *transferWriter) Write(p []byte) (int, error) {
	if err := w.transfer.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	w.transfer.progress.Bytes += int64(n)
	w.transfer.progress.FileBytes += int64(n)
	if w.transfer.report != nil {
		w.transfer.report(w.transfer.progress)
	}
	return n, err
}
