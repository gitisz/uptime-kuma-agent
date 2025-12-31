package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Logger *logrus.Logger

// Default values
const (
	DefaultLogLevel   = "info"
	DefaultLogFormat  = "text"
	DefaultLogFile    = "/var/log/uptime-kuma-agent/app.log"
	DefaultMaxSize    = 10 // MB
	DefaultMaxAge     = 30 // days
	DefaultMaxBackups = 5
	DefaultCompress   = true
)

// InitLogger initializes the global logger with configuration
func InitLogger(cfg *config.LoggingConfig) error {
	Logger = logrus.New()

	// Set log level with precedence: CLI flag > env var > config > default
	level := getLogLevel()
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("invalid log level '%s': %w", level, err)
	}
	Logger.SetLevel(logLevel)

	// Set formatter
	format := getLogFormat()
	switch strings.ToLower(format) {
	case "json":
		Logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05Z07:00",
		})
	default:
		Logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02T15:04:05Z07:00",
		})
	}

	// Set output
	logFile := getLogFile()
	if logFile != "" {
		// Ensure log directory exists
		logDir := filepath.Dir(logFile)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory %s: %w", logDir, err)
		}

		// Configure log rotation
		Logger.SetOutput(&lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    getMaxSize(),
			MaxAge:     getMaxAge(),
			MaxBackups: getMaxBackups(),
			Compress:   getCompress(),
		})
	} else {
		// Log to stdout/stderr
		Logger.SetOutput(os.Stdout)
	}

	return nil
}

// Precedence functions (CLI flag > env var > config > default)

// getLogLevel returns log level with proper precedence
func getLogLevel() string {
	// CLI flag takes highest precedence
	if level := os.Getenv("UPTIME_KUMA_AGENT_LOG_LEVEL"); level != "" {
		return level
	}
	// Then config
	if Logger != nil && Logger.Level != 0 {
		return Logger.Level.String()
	}
	// Default
	return DefaultLogLevel
}

// getLogFormat returns log format with proper precedence
func getLogFormat() string {
	if format := os.Getenv("UPTIME_KUMA_AGENT_LOG_FORMAT"); format != "" {
		return format
	}
	return DefaultLogFormat
}

// getLogFile returns log file path with proper precedence
func getLogFile() string {
	if file := os.Getenv("UPTIME_KUMA_AGENT_LOG_FILE"); file != "" {
		return file
	}
	return DefaultLogFile
}

// getMaxSize returns max size with proper precedence
func getMaxSize() int {
	if sizeStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_SIZE"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil {
			return size
		}
	}
	return DefaultMaxSize
}

// getMaxAge returns max age with proper precedence
func getMaxAge() int {
	if ageStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_AGE"); ageStr != "" {
		if age, err := strconv.Atoi(ageStr); err == nil {
			return age
		}
	}
	return DefaultMaxAge
}

// getMaxBackups returns max backups with proper precedence
func getMaxBackups() int {
	if backupsStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_BACKUPS"); backupsStr != "" {
		if backups, err := strconv.Atoi(backupsStr); err == nil {
			return backups
		}
	}
	return DefaultMaxBackups
}

// getCompress returns compress setting with proper precedence
func getCompress() bool {
	if compressStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_COMPRESS"); compressStr != "" {
		if compress, err := strconv.ParseBool(compressStr); err == nil {
			return compress
		}
	}
	return DefaultCompress
}

// Convenience functions for logging
func Debug(args ...interface{}) {
	if Logger != nil {
		Logger.Debug(args...)
	}
}

func Debugf(format string, args ...interface{}) {
	if Logger != nil {
		Logger.Debugf(format, args...)
	}
}

func Info(args ...interface{}) {
	if Logger != nil {
		Logger.Info(args...)
	}
}

func Infof(format string, args ...interface{}) {
	if Logger != nil {
		Logger.Infof(format, args...)
	}
}

func Warn(args ...interface{}) {
	if Logger != nil {
		Logger.Warn(args...)
	}
}

func Warnf(format string, args ...interface{}) {
	if Logger != nil {
		Logger.Warnf(format, args...)
	}
}

func Error(args ...interface{}) {
	if Logger != nil {
		Logger.Error(args...)
	}
}

func Errorf(format string, args ...interface{}) {
	if Logger != nil {
		Logger.Errorf(format, args...)
	}
}

func Fatal(args ...interface{}) {
	if Logger != nil {
		Logger.Fatal(args...)
	}
}

func Fatalf(format string, args ...interface{}) {
	if Logger != nil {
		Logger.Fatalf(format, args...)
	}
}
