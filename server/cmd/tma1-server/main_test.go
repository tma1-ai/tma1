package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantExit        int
		wantStdoutSub   string
		wantStderrSub   string
		wantStdoutEmpty bool
		wantStderrEmpty bool
	}{
		{
			name: "--help prints root help to stdout",
			args: []string{"tma1-server", "--help"}, wantExit: 0,
			wantStdoutSub: "Usage:", wantStderrEmpty: true,
		},
		{
			name: "help prints root help to stdout",
			args: []string{"tma1-server", "help"}, wantExit: 0,
			wantStdoutSub: "Usage:", wantStderrEmpty: true,
		},
		{
			name: "help install prints install help",
			args: []string{"tma1-server", "help", "install"}, wantExit: 0,
			wantStdoutSub: "--adapter", wantStderrEmpty: true,
		},
		{
			name: "help build prints build help",
			args: []string{"tma1-server", "help", "build"}, wantExit: 0,
			wantStdoutSub: "--watch", wantStderrEmpty: true,
		},
		{
			name: "help uninstall prints uninstall help",
			args: []string{"tma1-server", "help", "uninstall"}, wantExit: 0,
			wantStdoutSub: "--purge-data", wantStderrEmpty: true,
		},
		{
			name: "help mcp-serve prints mcp-serve help",
			args: []string{"tma1-server", "help", "mcp-serve"}, wantExit: 0,
			wantStdoutSub: "JSON-RPC", wantStderrEmpty: true,
		},
		{
			name: "help with unknown topic errors to stderr",
			args: []string{"tma1-server", "help", "bogus"}, wantExit: 2,
			wantStdoutEmpty: true, wantStderrSub: "unknown help topic",
		},
		{
			name: "install --help short-circuits to printer",
			args: []string{"tma1-server", "install", "--help"}, wantExit: 0,
			wantStdoutSub: "--skip-project-files", wantStderrEmpty: true,
		},
		{
			name: "build --help short-circuits to printer",
			args: []string{"tma1-server", "build", "--help"}, wantExit: 0,
			wantStdoutSub: "--debounce", wantStderrEmpty: true,
		},
		{
			name: "--version prints version to stdout",
			args: []string{"tma1-server", "--version"}, wantExit: 0,
			// Pins both the program name and the Version string so a
			// regression that emits help here would fail.
			wantStdoutSub: "tma1-server " + Version, wantStderrEmpty: true,
		},
		{
			name: "version prints version to stdout",
			args: []string{"tma1-server", "version"}, wantExit: 0,
			wantStdoutSub: "tma1-server " + Version, wantStderrEmpty: true,
		},
		{
			name: "unknown subcommand errors to stderr with exit 2",
			args: []string{"tma1-server", "bogus"}, wantExit: 2,
			wantStdoutEmpty: true, wantStderrSub: "unknown subcommand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := dispatch(tt.args, &stdout, &stderr)
			if got != tt.wantExit {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
					got, tt.wantExit, stdout.String(), stderr.String())
			}
			if tt.wantStdoutSub != "" && !strings.Contains(stdout.String(), tt.wantStdoutSub) {
				t.Errorf("stdout missing %q\nfull stdout:\n%s", tt.wantStdoutSub, stdout.String())
			}
			if tt.wantStderrSub != "" && !strings.Contains(stderr.String(), tt.wantStderrSub) {
				t.Errorf("stderr missing %q\nfull stderr:\n%s", tt.wantStderrSub, stderr.String())
			}
			if tt.wantStdoutEmpty && stdout.Len() != 0 {
				t.Errorf("stdout should be empty, got:\n%s", stdout.String())
			}
			if tt.wantStderrEmpty && stderr.Len() != 0 {
				t.Errorf("stderr should be empty, got:\n%s", stderr.String())
			}
		})
	}
}

func TestHasHelpFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"no flags", []string{"make", "test"}, false},
		{"--help present", []string{"--watch", "--help"}, true},
		{"-h present", []string{"-h"}, true},
		{"--help after -- belongs to wrapped command", []string{"--", "make", "--help"}, false},
		{"-h after -- belongs to wrapped command", []string{"--", "tool", "-h"}, false},
		{"--help before -- still ours", []string{"--watch", "--help", "--", "make"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasHelpFlag(tt.args); got != tt.want {
				t.Errorf("hasHelpFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestHasBuildHelpFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"--help present before command", []string{"--watch", "--help"}, true},
		{"-h present before command", []string{"-h"}, true},
		{"--help as flag value belongs to that flag", []string{"--debounce", "--help"}, false},
		{"--help after unknown token belongs to wrapped command", []string{"--unknown", "--help"}, false},
		{"--help after -- belongs to wrapped command", []string{"--", "make", "--help"}, false},
		{"-h after -- belongs to wrapped command", []string{"--", "tool", "-h"}, false},
		{"--help after command belongs to wrapped command", []string{"make", "--help"}, false},
		{"-h after command belongs to wrapped command", []string{"go", "test", "-h"}, false},
		{"--help before -- still ours", []string{"--watch", "--help", "--", "make"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBuildHelpFlag(tt.args); got != tt.want {
				t.Errorf("hasBuildHelpFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
