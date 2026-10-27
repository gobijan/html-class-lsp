package main

import (
	"io"
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stdin  string
		code   int
		stdout string
		stderr string
	}{
		{"serves until end of input", nil, "", 0, "", ""},
		{"accepts --stdio", []string{"--stdio"}, frame(`{"id":1,"method":"shutdown"}`) + frame(`{"method":"exit"}`), 0, `"result":null`, ""},
		{"version", []string{"--version"}, "", 0, "(devel)\n", ""},
		{"help", []string{"-h"}, "", 0, "", "Usage of html-class-lsp"},
		{"unknown flag", []string{"--bogus"}, "", 2, "", "flag provided but not defined: -bogus"},
		{"protocol error", nil, frame("{"), 1, "", "html-class-lsp: invalid message"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := run(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr)
			if code != tt.code {
				t.Errorf("exit code %d, want %d", code, tt.code)
			}
			if !strings.Contains(stdout.String(), tt.stdout) {
				t.Errorf("stdout %q, want it to contain %q", stdout.String(), tt.stdout)
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("stderr %q, want it to contain %q", stderr.String(), tt.stderr)
			}
		})
	}
}

func TestRunVersionWriteError(t *testing.T) {
	if code := run([]string{"--version"}, strings.NewReader(""), failingWriter{}, io.Discard); code != 1 {
		t.Errorf("exit code %d when stdout fails, want 1", code)
	}
}

func TestMainExitCode(t *testing.T) {
	code := -1
	exit = func(c int) { code = c }
	args := os.Args
	os.Args = []string{"html-class-lsp", "--bogus"}
	stderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = devNull
	t.Cleanup(func() {
		exit, os.Args, os.Stderr = os.Exit, args, stderr
		_ = devNull.Close()
	})

	main()
	if code != 2 {
		t.Errorf("main exited with %d, want 2", code)
	}
}

func TestModuleVersion(t *testing.T) {
	tests := []struct {
		info *debug.BuildInfo
		want string
	}{
		{nil, "(devel)"},
		{&debug.BuildInfo{}, "(devel)"},
		{&debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
	}
	for _, tt := range tests {
		if got := moduleVersion(tt.info); got != tt.want {
			t.Errorf("moduleVersion(%+v) = %q, want %q", tt.info, got, tt.want)
		}
	}
}
