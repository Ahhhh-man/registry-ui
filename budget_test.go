//go:build budget

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Ahhhh-man/registry-ui/internal/registry/registrytest"
)

// The idle budgets, checked on the release binary against a large registry
// in both modes. Run with `task budget`.
const (
	budgetRepos = 1000
	budgetTags  = 5 // per repository, so 5,000 in all
	maxIdleMem  = 15 * 1000 * 1000
	// idleWait outlasts the 90 s the registry client keeps an idle
	// connection open, after which nothing should be left running.
	idleWait = 100 * time.Second
)

func TestBudget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	bin := filepath.Join(t.TempDir(), "registry-ui")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	t.Run("api", func(t *testing.T) {
		t.Parallel()
		reg := registrytest.NewLarge(t, budgetRepos, budgetTags)
		measure(t, bin, "REGISTRY_UI_REGISTRY_URL="+reg.URL)
	})
	t.Run("storage", func(t *testing.T) {
		t.Parallel()
		reg := registrytest.NewLargeStorage(t, budgetRepos, budgetTags)
		measure(t, bin, "REGISTRY_UI_STORAGE_PATH="+reg.StoragePath, "REGISTRY_UI_PULL_URL=registry.example.com")
	})
}

// measure starts bin, browses every repository the way a visitor could,
// leaves it idle, and checks what it holds and runs afterwards.
func measure(t *testing.T, bin string, env ...string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = append(env, "REGISTRY_UI_LISTEN="+addr, "REGISTRY_UI_LOG_LEVEL=warn", "GOTRACEBACK=all")
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	pid := cmd.Process.Pid
	for start := time.Now(); ; time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("not listening on %s:\n%s", addr, &stderr)
		}
	}
	t.Logf("after start: %s", memory(t, pid))

	client := &http.Client{}
	get := func(path string) {
		resp, err := client.Get("http://" + addr + "/" + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /%s: %s", path, resp.Status)
		}
	}
	// The catalog in each view, and in each order and filter that loads
	// every repository's details; then each repository sorted by created,
	// which loads every tag, and its first tag's page.
	for _, q := range []string{"", "?view=compact", "?view=detailed", "?sort=created", "?sort=tags", "?type=image"} {
		get(q)
	}
	for i := range budgetRepos {
		repo := registrytest.LargeRepo(i)
		get(repo)
		get(repo + "?sort=created")
		get(repo + ":" + registrytest.LargeTag(0))
	}
	t.Logf("after browsing: %s", memory(t, pid))
	client.CloseIdleConnections()

	busy := cpuTicks(t, pid)
	time.Sleep(idleWait)
	ticks := cpuTicks(t, pid) - busy
	t.Logf("idle for %s: %s, CPU %d ms", idleWait, memory(t, pid), ticks*10)
	if anon := status(t, pid, "RssAnon"); anon >= maxIdleMem {
		t.Errorf("idle private memory is %s, over the %s budget", mb(anon), mb(maxIdleMem))
	}

	// SIGQUIT makes the runtime print every goroutine and exit. Idle, the
	// only one of ours left should be the server waiting for a connection;
	// the runtime's own (GC workers and the like) are left out.
	_ = cmd.Process.Signal(syscall.SIGQUIT)
	_ = cmd.Wait()
	var ours []string
	for _, g := range strings.Split(stderr.String(), "\n\n") {
		if strings.HasPrefix(g, "goroutine ") && !strings.HasPrefix(g, "goroutine 0 ") && !strings.Contains(g, "\ncreated by runtime.") {
			ours = append(ours, g)
		}
	}
	t.Logf("idle goroutines: %d", len(ours))
	if len(ours) != 1 || !strings.Contains(ours[0], "(*TCPListener).Accept") {
		t.Errorf("want only the listener waiting when idle, got:\n%s", strings.Join(ours, "\n\n"))
	}
}

// status reads a size field, such as VmRSS, from pid's /proc status, in bytes.
func status(t *testing.T, pid int, field string) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb * 1024
		}
	}
	t.Fatalf("no %s in /proc status", field)
	return 0
}

// memory describes pid's resident memory: the private part, which is what
// it allocated, and the rest, which is mostly the executable's own pages.
func memory(t *testing.T, pid int) string {
	rss, anon := status(t, pid, "VmRSS"), status(t, pid, "RssAnon")
	return fmt.Sprintf("RSS %s (private %s, executable and other files %s)", mb(rss), mb(anon), mb(rss-anon))
}

// cpuTicks is the user and system CPU time pid has used, in clock ticks
// (10 ms on Linux).
func cpuTicks(t *testing.T, pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	// Fields after the command name, which is in parentheses and may hold spaces.
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
	utime, _ := strconv.ParseInt(f[11], 10, 64)
	stime, _ := strconv.ParseInt(f[12], 10, 64)
	return utime + stime
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }
