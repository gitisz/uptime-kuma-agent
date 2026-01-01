package logging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Logger *logrus.Logger

// CustomFormatter formats logs like: 2025-09-14 10:22:41.812 - [DEBUG]: Running command...
type CustomFormatter struct{}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.UTC().Format("2006-01-02T15:04:05.000Z")
	level := strings.ToUpper(entry.Level.String())
	msg := entry.Message

	// Format: 2025-12-31T12:00:48.123Z - [DEBUG]: Running command...
	return []byte(fmt.Sprintf("%s - [%s]: %s\n", timestamp, level, msg)), nil
}

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

	// Set log level with precedence: env var > config > default
	level := getLogLevel(cfg)
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("invalid log level '%s': %w", level, err)
	}
	Logger.SetLevel(logLevel)

	// Set formatter
	format := getLogFormat(cfg)
	switch strings.ToLower(format) {
	case "json":
		Logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05Z07:00",
		})
	default:
		// Custom format: 2025-09-14 10:22:41.812 - [DEBUG]: Running command...
		Logger.SetFormatter(&CustomFormatter{})
	}

	// Set output
	logFile := GetLogFile(cfg)
	if logFile != "" {
		// Ensure log directory exists
		logDir := filepath.Dir(logFile)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory %s: %w", logDir, err)
		}

		// Configure log rotation for our application logger
		lumberjackLogger := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    getMaxSize(cfg),
			MaxAge:     getMaxAge(cfg),
			MaxBackups: getMaxBackups(cfg),
			Compress:   getCompress(cfg),
		}
		Logger.SetOutput(lumberjackLogger)

		// Configure GLOBAL logrus for external libraries (Socket.IO client)
		logrus.SetOutput(lumberjackLogger)      // Same file!
		logrus.SetLevel(logLevel)               // Same level
		logrus.SetFormatter(&CustomFormatter{}) // Same format

		// Configure standard Go logger for any libraries using log package
		log.SetOutput(lumberjackLogger) // Redirect standard log to file too
	} else {
		// Log to stdout/stderr
		Logger.SetOutput(os.Stdout)
		// Global logrus also to stdout for external libraries
		logrus.SetOutput(os.Stdout)
		logrus.SetLevel(logLevel)
		logrus.SetFormatter(&CustomFormatter{})
		// Standard Go logger also to stdout
		log.SetOutput(os.Stdout)
	}

	return nil
}

// Precedence functions (env var > config > default)

// getLogLevel returns log level with proper precedence: env var > config > default
func getLogLevel(cfg *config.LoggingConfig) string {
	// Environment variable
	if level := os.Getenv("UPTIME_KUMA_AGENT_LOG_LEVEL"); level != "" {
		return level
	}
	// Config file value
	if cfg != nil && cfg.Level != "" {
		return cfg.Level
	}
	// Default
	return DefaultLogLevel
}

// getLogFormat returns log format with proper precedence: env var > config > default
func getLogFormat(cfg *config.LoggingConfig) string {
	// Environment variable
	if format := os.Getenv("UPTIME_KUMA_AGENT_LOG_FORMAT"); format != "" {
		return format
	}
	// Config file value
	if cfg != nil && cfg.Format != "" {
		return cfg.Format
	}
	return DefaultLogFormat
}

// GetLogFile returns log file path with proper precedence: env var > config > default
func GetLogFile(cfg *config.LoggingConfig) string {
	// Environment variable
	if file := os.Getenv("UPTIME_KUMA_AGENT_LOG_FILE"); file != "" {
		return file
	}
	// Config file value - use InternalLogDirectory + hardcoded filename
	if cfg != nil && cfg.InternalLogDirectory != "" {
		return filepath.Join(cfg.InternalLogDirectory, "app.log")
	}
	return DefaultLogFile
}

// GetHostLogDirectory returns host log directory path with proper precedence: env var > config > default
func GetHostLogDirectory(cfg *config.LoggingConfig) string {
	// Environment variable
	if dir := os.Getenv("UPTIME_KUMA_AGENT_HOST_LOG_DIRECTORY"); dir != "" {
		return dir
	}
	// Config file value
	if cfg != nil && cfg.HostLogDirectory != "" {
		return cfg.HostLogDirectory
	}
	// Default to the directory of the default log file
	return filepath.Dir(DefaultLogFile)
}

// GetInternalLogDirectory returns internal log directory path with proper precedence: env var > config > default
func GetInternalLogDirectory(cfg *config.LoggingConfig) string {
	// Environment variable
	if dir := os.Getenv("UPTIME_KUMA_AGENT_INTERNAL_LOG_DIRECTORY"); dir != "" {
		return dir
	}
	// Config file value
	if cfg != nil && cfg.InternalLogDirectory != "" {
		return cfg.InternalLogDirectory
	}
	// Default internal log directory
	return "/app-logs"
}

// getMaxSize returns max size with proper precedence: env var > config > default
func getMaxSize(cfg *config.LoggingConfig) int {
	// Environment variable
	if sizeStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_SIZE"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil {
			return size
		}
	}
	// Config file value
	if cfg != nil && cfg.MaxSize > 0 {
		return cfg.MaxSize
	}
	return DefaultMaxSize
}

// getMaxAge returns max age with proper precedence: env var > config > default
func getMaxAge(cfg *config.LoggingConfig) int {
	// Environment variable
	if ageStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_AGE"); ageStr != "" {
		if age, err := strconv.Atoi(ageStr); err == nil {
			return age
		}
	}
	// Config file value
	if cfg != nil && cfg.MaxAge > 0 {
		return cfg.MaxAge
	}
	return DefaultMaxAge
}

// getMaxBackups returns max backups with proper precedence: env var > config > default
func getMaxBackups(cfg *config.LoggingConfig) int {
	// Environment variable
	if backupsStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_MAX_BACKUPS"); backupsStr != "" {
		if backups, err := strconv.Atoi(backupsStr); err == nil {
			return backups
		}
	}
	// Config file value
	if cfg != nil && cfg.MaxBackups > 0 {
		return cfg.MaxBackups
	}
	return DefaultMaxBackups
}

// getCompress returns compress setting with proper precedence: env var > config > default
func getCompress(cfg *config.LoggingConfig) bool {
	// Environment variable
	if compressStr := os.Getenv("UPTIME_KUMA_AGENT_LOG_COMPRESS"); compressStr != "" {
		if compress, err := strconv.ParseBool(compressStr); err == nil {
			return compress
		}
	}
	// Config file value
	if cfg != nil && cfg.Compress != nil {
		return *cfg.Compress
	}
	return DefaultCompress
}

// GetSocketIOLogLevel returns Socket.IO log level with proper precedence: env var > config > default
func GetSocketIOLogLevel(cfg *config.LoggingConfig) string {
	// Environment variable
	if level := os.Getenv("UPTIME_KUMA_AGENT_SOCKETIO_LOG_LEVEL"); level != "" {
		return level
	}
	// Config file value
	if cfg != nil && cfg.SocketIOLogLevel != "" {
		return cfg.SocketIOLogLevel
	}
	// Default: "warn" (to reduce Socket.IO connection noise)
	return "warn"
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
