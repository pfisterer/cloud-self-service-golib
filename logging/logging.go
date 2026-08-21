// Package logging builds the zap logger the platform's services share.
//
// The three services carried byte-for-byte the same file under three names
// (logger.go, logging_helpers.go) with two names for the same caller encoder.
// Nothing here is novel; it is the copy that stops being copied.
//
// Deliberately no gin dependency: the gin middleware that two of the three
// copies carried (InjectLoggerMiddleware, LoggerKey) was called from nowhere,
// so it did not come along. That keeps zap the only thing this module pulls in.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Init returns the logger and its sugared counterpart. Callers keep both: the
// unsugared one to zap.Logger-typed constructors and Sync, the sugared one to
// everything else.
//
// In development mode this is a coloured console logger at debug level with no
// timestamp — a terminal already shows when a line appeared, and the column
// only pushes the message to the right. In production it is zap's JSON
// production logger, which is what the log collector expects.
func Init(devMode bool) (*zap.Logger, *zap.SugaredLogger) {
	var (
		logger *zap.Logger
		err    error
	)

	if devMode {
		encoderConfig := zapcore.EncoderConfig{
			MessageKey:     "msg",
			LevelKey:       "level",
			TimeKey:        "",
			NameKey:        "logger",
			CallerKey:      "caller",
			EncodeLevel:    zapcore.CapitalColorLevelEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   callerEncoder,
		}
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			os.Stdout,
			zap.DebugLevel,
		)
		// zap.AddCaller is what makes callerEncoder see anything at all.
		logger = zap.New(core, zap.AddCaller())
	} else {
		logger, err = zap.NewProduction()
	}

	if err != nil {
		panic(fmt.Errorf("logging.Init: %w", err))
	}

	return logger, logger.Sugar()
}

// callerEncoder renders the call site as "(file.go:42) FunctionName", dropping
// the package path from the function name. Full paths in a development console
// are noise: the file name already says where to look.
func callerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	fn := caller.Function
	if idx := strings.LastIndexByte(fn, '.'); idx != -1 {
		fn = fn[idx+1:]
	}
	enc.AppendString(fmt.Sprintf("(%s:%d) %s", filepath.Base(caller.File), caller.Line, fn))
}

// Writer adapts a zap logger to io.Writer, so that libraries which insist on
// writing to one — gin's default output, the standard log package — end up in
// the same stream as everything else instead of bare on stdout.
type Writer struct {
	Logger *zap.SugaredLogger
	Level  zapcore.Level
}

// Write logs one message per call, trimmed. Empty writes are swallowed: the
// writers this stands in for terminate their lines, which would otherwise
// produce an empty log entry after every real one.
func (w *Writer) Write(p []byte) (int, error) {
	s := strings.TrimSpace(string(p))
	if s == "" {
		return len(p), nil
	}

	switch w.Level {
	case zapcore.DebugLevel:
		w.Logger.Debug(s)
	case zapcore.WarnLevel:
		w.Logger.Warn(s)
	case zapcore.ErrorLevel:
		w.Logger.Error(s)
	default:
		w.Logger.Info(s)
	}
	return len(p), nil
}
