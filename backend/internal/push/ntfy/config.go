// Package ntfy pushes the notifications that need the owner to their phone
// through an ntfy server (LADO fork, 2026-11-27). AO's own phone push goes
// only to a paired AO Mobile app; the owner reads ntfy, and the Claude Code
// hook that used to push there rang on every finished turn of every session.
//
// Only what needs the owner goes out: a session waiting on its input, a PR
// ready to merge, and the LADO alerts of a stalled worker, an account at its
// usage limit, an agent that cannot start and a conflict LADO could not
// resolve. ntfy.sh is a public server, so a message names only the project,
// the issue number, the session id and what happened: never a notification's
// own title or body, a prompt or an agent's reply.
package ntfy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ConfigFile is the settings file in AO's data directory. Without it the
// dispatcher stays off.
const ConfigFile = "ntfy.json"

// DefaultServer is the server a config without one publishes to.
const DefaultServer = "https://ntfy.sh"

// Config is where messages go. The topic is a shared secret of sorts: anyone
// who knows it reads the messages, so it lives only in the config file.
type Config struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
}

// topicPattern is what ntfy accepts as a topic name.
var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// LoadConfig reads <dataDir>/ntfy.json. ok is false, with no error, when the
// file does not exist; a file that is there but wrong is an error.
func LoadConfig(dataDir string) (Config, bool, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read %s: %w", ConfigFile, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	cfg.Topic = strings.TrimSpace(cfg.Topic)
	cfg.Server = strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	if cfg.Server == "" {
		cfg.Server = DefaultServer
	}
	if !topicPattern.MatchString(cfg.Topic) {
		return Config{}, false, fmt.Errorf("%s: topic must be 1 to 64 letters, digits, - or _", ConfigFile)
	}
	u, err := url.Parse(cfg.Server)
	plainLocal := u != nil && u.Scheme == "http" && loopback(u.Hostname())
	if err != nil || u.Host == "" || (u.Scheme != "https" && !plainLocal) {
		return Config{}, false, fmt.Errorf("%s: server must be an https URL, or http on this computer", ConfigFile)
	}
	return cfg, true, nil
}

// loopback reports whether host is this computer: the topic travels in the
// request body, so plain http may only stay on it.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
