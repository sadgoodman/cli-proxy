package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTunnelStartupWithoutCA(t *testing.T) {
	if os.Getenv("CLI_PROXY_TEST_TUNNEL") == "1" {
		os.Args = []string{"cli-proxy", "-tunnel", "-headless", "-addr", "127.0.0.1:0"}
		main()
		os.Exit(0)
	}
	for _, invalidDir := range []bool{false, true} {
		name := "missing-directory"
		if invalidDir {
			name = "invalid-directory"
		}
		t.Run(name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "proxy-home")
			if invalidDir {
				if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTunnelStartupWithoutCA$")
			cmd.Env = append(os.Environ(), "CLI_PROXY_TEST_TUNNEL=1", "CLI_PROXY_HOME="+home)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}()
			scanner := bufio.NewScanner(out)
			if !scanner.Scan() || !strings.Contains(scanner.Text(), "(tunnel)") {
				t.Fatalf("proxy did not start in tunnel mode: %q, %v", scanner.Text(), scanner.Err())
			}
			if !scanner.Scan() || !strings.Contains(scanner.Text(), "no CA certificate required") {
				t.Fatalf("unexpected startup instructions: %q", scanner.Text())
			}
			if !invalidDir {
				if _, err := os.Stat(home); !os.IsNotExist(err) {
					t.Fatalf("tunnel startup created CA directory: %v", err)
				}
			}
		})
	}
}

func TestResolveAddr(t *testing.T) {
	cases := []struct {
		addr    string
		port    int
		want    string
		wantErr bool
	}{
		{"0.0.0.0:8080", 0, "0.0.0.0:8080", false},
		{"0.0.0.0:8080", 9090, "0.0.0.0:9090", false},
		{"127.0.0.1:8080", 9090, "127.0.0.1:9090", false},
		{"127.0.0.1", 9090, "127.0.0.1:9090", false},
		{":8080", 9090, "0.0.0.0:9090", false},
		{"[::1]:8080", 9090, "[::1]:9090", false},
		{"0.0.0.0:8080", 1, "0.0.0.0:1", false},
		{"0.0.0.0:8080", 65535, "0.0.0.0:65535", false},
		{"0.0.0.0:8080", -1, "", true},
		{"0.0.0.0:8080", 70000, "", true},
	}
	for _, tc := range cases {
		got, err := resolveAddr(tc.addr, tc.port)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveAddr(%q, %d) should have failed, got %q", tc.addr, tc.port, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveAddr(%q, %d): %v", tc.addr, tc.port, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolveAddr(%q, %d) = %q, want %q", tc.addr, tc.port, got, tc.want)
		}
	}
}
