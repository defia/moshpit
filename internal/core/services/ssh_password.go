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
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// OpenSSH's askpass reader accepts up to 1023 bytes before its terminating NUL.
const maxSSHPasswordBytes = 1023

type passwordTarget struct {
	host, port, user, hostKeyAlias string
}

func (t passwordTarget) binding() string {
	return net.JoinHostPort(t.host, t.port) + "\n" + t.user + "\n" + t.hostKeyAlias
}

func (t passwordTarget) matchesPrompt(prompt string) bool {
	host := t.host
	if t.hostKeyAlias != "" {
		host = t.hostKeyAlias
	}
	// Older OpenSSH clients truncate the username and hostname in this prompt.
	return prompt == fmt.Sprintf("%s@%s's password: ", t.user, host) ||
		prompt == fmt.Sprintf("%.30s@%.128s's password: ", t.user, host)
}

func validSSHPassword(password []byte) bool {
	return len(password) > 0 && len(password) <= maxSSHPasswordBytes &&
		!bytes.ContainsAny(password, "\x00\r\n")
}

func (s *serverService) PasswordStorageAvailable() bool { return s.passwords != nil }

func (s *serverService) SaveSSHPassword(alias string, password []byte) error {
	if !s.PasswordStorageAvailable() {
		return errors.New("password storage requires a macOS Keychain-enabled build")
	}
	if !validSSHPassword(password) {
		return fmt.Errorf("enter a password of 1 to %d bytes without line breaks", maxSSHPasswordBytes)
	}
	target, err := resolvePasswordTarget(alias)
	if err != nil {
		return err
	}
	return s.passwords.Save(alias, target.binding(), password)
}

func (s *serverService) DeleteSSHPassword(alias string) error {
	if !s.PasswordStorageAvailable() {
		return errors.New("password storage requires a macOS Keychain-enabled build")
	}
	if !validPasswordAlias(alias) {
		return errors.New("this SSH alias cannot use a saved password")
	}
	return s.passwords.Delete(alias)
}

func resolvePasswordTarget(alias string) (passwordTarget, error) {
	if !validPasswordAlias(alias) {
		return passwordTarget{}, errors.New("this SSH alias cannot use a saved password")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- the alias is validated and follows the option terminator.
	out, err := exec.CommandContext(ctx, "ssh", "-G", "--", alias).Output()
	if err != nil {
		return passwordTarget{}, fmt.Errorf("could not resolve the SSH configuration for %s: %w", alias, err)
	}
	return parsePasswordTarget(string(out))
}

func validPasswordAlias(alias string) bool {
	return alias != "" && !strings.HasPrefix(alias, "-") &&
		strings.IndexFunc(alias, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-')
		}) == -1
}

func parsePasswordTarget(config string) (passwordTarget, error) {
	options := make(map[string]string)
	for _, line := range strings.Split(config, "\n") {
		key, value, ok := strings.Cut(line, " ")
		if ok {
			options[key] = value
		}
	}

	if options["batchmode"] == "yes" || options["passwordauthentication"] == "no" {
		return passwordTarget{}, errors.New("ssh configuration disables password prompts for this host")
	}
	// Proxy subprocesses inherit the SSH environment. Keep the askpass token
	// scoped to a direct SSH connection.
	for _, name := range []string{"proxyjump", "proxycommand", "knownhostscommand"} {
		if value := options[name]; value != "" && value != "none" {
			return passwordTarget{}, errors.New("saved passwords require direct SSH with a trusted host key")
		}
	}

	target := passwordTarget{
		host: options["hostname"], port: options["port"], user: options["user"],
		hostKeyAlias: options["hostkeyalias"],
	}
	port, err := strconv.Atoi(target.port)
	if err != nil || port < 1 || port > 65535 || target.host == "" || target.user == "" {
		return passwordTarget{}, errors.New("ssh did not resolve a valid host, port, and username")
	}
	if !utf8.ValidString(target.binding()) || strings.ContainsAny(target.binding(), "\x00\r") {
		return passwordTarget{}, errors.New("ssh returned invalid destination text")
	}
	return target, nil
}

func (s *serverService) connectionWithSavedPassword(alias string, options []string, subsystem ...string) (*exec.Cmd, func(), error) {
	if !s.PasswordStorageAvailable() || !validPasswordAlias(alias) {
		return nil, nil, nil
	}
	hasPassword, err := s.passwords.Has(alias, "")
	if err != nil || !hasPassword {
		return nil, nil, err
	}
	target, err := resolvePasswordTarget(alias)
	if err != nil {
		// Configuration changes may make an existing saved password ineligible.
		return nil, nil, nil //nolint:nilerr // fall back to regular SSH
	}
	binding := target.binding()
	matches, err := s.passwords.Has(alias, binding)
	if err != nil || !matches {
		return nil, nil, err
	}

	// Pin the resolved target for this invocation and limit the helper to
	// SSH's password method, with mandatory host authentication.
	args := []string{
		"-o", "Hostname=" + target.host, "-o", "CanonicalizeHostname=no",
		"-p", target.port, "-l", target.user,
		"-o", "PreferredAuthentications=password", "-o", "KbdInteractiveAuthentication=no",
		"-o", "NumberOfPasswordPrompts=1", "-o", "StrictHostKeyChecking=yes",
		"-o", "NoHostAuthenticationForLocalhost=no", "-o", "VerifyHostKeyDNS=no",
		"-o", "ProxyCommand=none", "-o", "KnownHostsCommand=none",
		"-o", "ControlPath=none", "-o", "ControlMaster=no",
	}
	if target.hostKeyAlias != "" {
		args = append(args, "-o", "HostKeyAlias="+target.hostKeyAlias)
	}
	args = append(args, options...)
	args = append(args, "--", alias)
	args = append(args, subsystem...)
	// #nosec G204 -- options and operands are separate arguments to the system SSH client.
	cmd := exec.Command("ssh", args...)
	cleanup, err := attachAskpass(cmd, target, func() ([]byte, error) {
		return s.passwords.Read(alias, binding)
	})
	if err != nil {
		return nil, nil, err
	}
	return cmd, cleanup, nil
}
