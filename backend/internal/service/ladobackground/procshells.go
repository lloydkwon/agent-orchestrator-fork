package ladobackground

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ProcShells counts the background shells under a session's chat host from
// the Linux process table: the shells Claude Code's Bash tool starts source
// a ~/.claude/shell-snapshots script. A resting worker runs no foreground
// tool, so every such shell under it is background work. One walk of the
// table serves every session for TTL.
type ProcShells struct {
	// Root is the process table; "" is /proc.
	Root string
	// TTL is how long one walk serves; 0 is 15 seconds.
	TTL time.Duration

	mu     sync.Mutex
	walked time.Time
	ok     bool
	// shells counts the background shells by session, for every session
	// with a chat host.
	shells map[string]int
}

const defaultProcTTL = 15 * time.Second

// LiveBackgroundShells counts them; ok is false off Linux, without a
// readable process table, or without the session's chat host.
func (p *ProcShells) LiveBackgroundShells(session domain.SessionID) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ttl := p.TTL
	if ttl <= 0 {
		ttl = defaultProcTTL
	}
	if p.walked.IsZero() || time.Since(p.walked) >= ttl {
		p.shells, p.ok = p.walk()
		p.walked = time.Now()
	}
	if !p.ok {
		return 0, false
	}
	n, ok := p.shells[string(session)]
	return n, ok
}

func (p *ProcShells) walk() (map[string]int, bool) {
	root := p.Root
	if root == "" {
		if runtime.GOOS != "linux" {
			return nil, false
		}
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	children := map[int][]int{}
	cmdlines := map[int]string{}
	hosts := map[int]string{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, e.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, ok := parentOf(stat)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		cmdlines[pid] = strings.Join(argv, " ")
		if len(argv) >= 3 && argv[1] == "chat-host" {
			hosts[pid] = argv[2]
		}
	}
	shells := make(map[string]int, len(hosts))
	for host, session := range hosts {
		n := 0
		queue := []int{host}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			for _, child := range children[pid] {
				if strings.Contains(cmdlines[child], "/.claude/shell-snapshots/") {
					n++
					continue // its own children are the command, not another shell
				}
				queue = append(queue, child)
			}
		}
		shells[session] += n
	}
	return shells, true
}

// parentOf reads the parent pid from a /proc/<pid>/stat line; the command
// name in parentheses may hold spaces and parentheses.
func parentOf(stat []byte) (int, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(string(stat[end+2:]))
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}
