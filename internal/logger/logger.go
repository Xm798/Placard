package logger

import (
	"os"
	"sync/atomic"

	"github.com/Xm798/placard/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/lumberjack.v2"
)

var globalLogger atomic.Pointer[zap.Logger]

// Init installs the global logger. See newCore for the sinks it opens.
func Init(cfg config.LogConfig) {
	globalLogger.Store(zap.New(newCore(cfg), zap.AddCaller()))
}

// newCore builds the sinks. The console on stdout is always one of them: it is
// what `docker logs` and journalctl show, and a container that will not finish
// starting is exactly when an operator has no other way in. With log.file set
// a second, JSON-encoded copy goes to the rotated file — the machine-readable
// one, for a collector that reads files rather than the container's stream.
//
// Both are bounded, which is what makes two sinks affordable: lumberjack caps
// the file at max_size × max_backups, and the stream is capped by whatever
// consumes it (the shipped docker-compose sets json-file max-size, journald
// has its own limits). An instance whose stream goes to an uncapped collector
// can still turn the console copy off by pointing log.file at a file and
// running with stdout redirected to /dev/null.
func newCore(cfg config.LogConfig) zapcore.Core {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	console := zapcore.NewCore(
		zapcore.NewConsoleEncoder(newConsoleEncoderConfig()),
		zapcore.AddSync(os.Stdout),
		level,
	)
	if cfg.File == "" {
		return console
	}

	syncer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
		Compress:   cfg.Compress,
	})
	file := zapcore.NewCore(zapcore.NewJSONEncoder(newFileEncoderConfig()), syncer, level)
	return zapcore.NewTee(file, console)
}

func newConsoleEncoderConfig() zapcore.EncoderConfig {
	cfg := zap.NewDevelopmentEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncodeLevel = zapcore.CapitalLevelEncoder
	if stdoutIsTTY() {
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	return cfg
}

// stdoutIsTTY reports whether stdout is a terminal. Under `docker logs`, a
// systemd unit or a shell redirect the stream is a pipe or a file, where ANSI
// colour codes are noise in whatever stores the line.
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func newFileEncoderConfig() zapcore.EncoderConfig {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg
}

// Module returns a child logger tagged with the given module name.
func Module(name string) *zap.Logger {
	return L().Named(name)
}

// L returns the global logger.
func L() *zap.Logger {
	if l := globalLogger.Load(); l != nil {
		return l
	}
	// Fallback before Init is called (e.g. during config loading).
	fallback, _ := zap.NewProduction()
	globalLogger.CompareAndSwap(nil, fallback)
	return globalLogger.Load()
}

// S returns the global sugared logger.
func S() *zap.SugaredLogger {
	return L().Sugar()
}

// Sync flushes any buffered log entries.
func Sync() {
	if l := globalLogger.Load(); l != nil {
		_ = l.Sync()
	}
}
