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

package ports

import (
	"time"

	"github.com/taylorbanks/moshpit/internal/core/domain"
)

type ServerService interface {
	ListServers(query string) ([]domain.Server, error)
	UpdateServer(server domain.Server, newServer domain.Server) error
	AddServer(server domain.Server) error
	DeleteServer(server domain.Server) error
	SetPinned(alias string, pinned bool) error
	SetProtocol(alias string, protocol string) error
	SSH(alias string) error
	SSHManual(alias string) error
	OpenSFTP(alias string, manual bool) (SFTPSession, error)
	PasswordStorageAvailable() bool
	SaveSSHPassword(alias string, password []byte) error
	DeleteSSHPassword(alias string) error
	SSHWithArgs(alias string, extraArgs []string) error
	StartForward(alias string, extraArgs []string) (int, error)
	StopForwarding(alias string) error
	IsForwarding(alias string) bool
	IsMoshAvailable() bool
	CopySSHKey(alias, keyPath string) error
	ListSSHKeys() ([]SSHKeyInfo, error)
	Ping(server domain.Server) (bool, time.Duration, error)
}

// PasswordStore stores at most one password per SSH alias. A binding identifies
// the resolved destination and user, and an empty binding checks only the alias.
type PasswordStore interface {
	Has(alias, binding string) (bool, error)
	Read(alias, binding string) ([]byte, error)
	Save(alias, binding string, password []byte) error
	Delete(alias string) error
}

// SSHKeyInfo describes a candidate public key the user can install on a remote
// server. Fingerprint is human-readable output from `ssh-keygen -lf` and may
// be empty if ssh-keygen is unavailable or the file isn't a key.
type SSHKeyInfo struct {
	Path        string
	DisplayName string
	Fingerprint string
}
