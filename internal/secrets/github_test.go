package secrets

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "ghs_testtoken"

func ghServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s.mint = func(context.Context, string) (string, error) { return testToken, nil }
	return s
}

// The token reaches gh through a hosts.yml in a private dir, never the
// environment, so nothing gh evaluates can read it back from env.
func TestGHTokenOnlyInPrivateHostsFile(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf 'env=%s hosts=%s mode=%s\n' "$(env | grep -c ` + testToken + `)" "$(grep -c ` + testToken + ` "$GH_CONFIG_DIR/hosts.yml")" "$(ls -ld "$GH_CONFIG_DIR" | cut -c1-10)"
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	resp := ghServer(t).gh(agent, Request{Project: "alpha", Args: []string{"pr", "view", "7", "--json", "isDraft"}})
	if resp.Error != "" || resp.Exit != 0 {
		t.Fatalf("gh: %+v", resp)
	}
	if want := "env=0 hosts=2 mode=drwx------\n"; resp.Stdout != want {
		t.Fatalf("stdout = %q, want %q", resp.Stdout, want)
	}
}

// Real gh authenticates from the hosts.yml the broker writes.
func TestGHRealCLIAuthenticatesFromHostsFile(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH")
	}
	sock := filepath.Join(t.TempDir(), "api.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var auth []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"filename":"a.go","patch":"+x"}]`)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	s := ghServer(t)
	s.ghSocket = sock
	resp := s.gh(agent, Request{Project: "alpha", Args: []string{"api", "repos/acme/alpha/pulls/7/files", "--hostname", "github.com", "--paginate"}})
	if resp.Error != "" || resp.Exit != 0 {
		t.Fatalf("gh: %+v", resp)
	}
	if !strings.Contains(resp.Stdout, `"filename":"a.go"`) {
		t.Fatalf("stdout = %q", resp.Stdout)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) == 0 || auth[0] != "token "+testToken {
		t.Fatalf("Authorization headers = %q", auth)
	}
}

// A reply cut at the broker's output cap is an error, not short JSON.
func TestGHTruncatedIsError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "b.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
		fmt.Fprintln(conn, `{"stdout":"[{\"filename\":","truncated":true}`)
	}()
	t.Setenv("SPORE_SECRETS_SOCKET", sock)
	if _, err := GH("alpha", "", []string{"api", "repos/acme/alpha/pulls/7/files"}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("err = %v, want the cap", err)
	}
}
