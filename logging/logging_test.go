package logging

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestInit(t *testing.T) {
	for _, devMode := range []bool{true, false} {
		logger, sugar := Init(devMode)
		if logger == nil || sugar == nil {
			t.Fatalf("Init(%v) returned nil", devMode)
		}
		// Production is JSON at info level, development is console at debug.
		// Debug being enabled is the observable difference between the two.
		if got := logger.Core().Enabled(zapcore.DebugLevel); got != devMode {
			t.Errorf("Init(%v): debug enabled = %v, want %v", devMode, got, devMode)
		}
	}
}

func TestWriter(t *testing.T) {
	cases := []struct {
		name  string
		level zapcore.Level
		want  zapcore.Level
	}{
		{name: "debug", level: zapcore.DebugLevel, want: zapcore.DebugLevel},
		{name: "warn", level: zapcore.WarnLevel, want: zapcore.WarnLevel},
		{name: "error", level: zapcore.ErrorLevel, want: zapcore.ErrorLevel},
		{name: "info", level: zapcore.InfoLevel, want: zapcore.InfoLevel},
		// Everything without its own case lands on info rather than being
		// dropped — a log line at an unexpected level is still a log line.
		{name: "unhandled level falls back to info", level: zapcore.FatalLevel, want: zapcore.InfoLevel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			w := &Writer{Logger: zap.New(core).Sugar(), Level: tc.level}

			n, err := w.Write([]byte("  gin says something\n"))
			if err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			// io.Writer contract: report every byte consumed, including the
			// trimmed ones, or the caller treats it as a short write.
			if n != len("  gin says something\n") {
				t.Errorf("Write() n = %d, want %d", n, len("  gin says something\n"))
			}

			entries := logs.All()
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if entries[0].Level != tc.want {
				t.Errorf("level = %v, want %v", entries[0].Level, tc.want)
			}
			if entries[0].Message != "gin says something" {
				t.Errorf("message = %q, want it trimmed", entries[0].Message)
			}
		})
	}
}

func TestWriterSwallowsEmptyLines(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	w := &Writer{Logger: zap.New(core).Sugar(), Level: zapcore.InfoLevel}

	for _, empty := range []string{"", "\n", "   ", " \t\n "} {
		if _, err := w.Write([]byte(empty)); err != nil {
			t.Fatalf("Write(%q) error = %v", empty, err)
		}
	}

	if n := logs.Len(); n != 0 {
		t.Errorf("got %d entries, want none", n)
	}
}
