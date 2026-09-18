package main

import "testing"

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
