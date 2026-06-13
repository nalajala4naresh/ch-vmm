package log

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

// LogType is type used for log type.
type LogType string

const (
	// AppLog is constant used for application debugging logs.
	AppLog LogType = "APPLOG"
)

// Fields type is used to pass to `WithFields`.
type Fields = logrus.Fields

// Level is logger log level.
type Level uint32

// Convert the Level to a string. E.g. PanicLevel becomes "panic".
func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warning"
	case ErrorLevel:
		return "error"
	case FatalLevel:
		return "fatal"
	case PanicLevel:
		return "panic"
	}

	return "unknown"
}

// ParseLevel takes a string level and returns the log level constant.
func ParseLevel(lvl string) (Level, error) {
	switch strings.ToLower(lvl) {
	case "panic":
		return PanicLevel, nil
	case "fatal":
		return FatalLevel, nil
	case "error":
		return ErrorLevel, nil
	case "warn", "warning":
		return WarnLevel, nil
	case "info":
		return InfoLevel, nil
	case "debug":
		return DebugLevel, nil
	}

	var l Level
	return l, fmt.Errorf("not a valid Level: %q", lvl)
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (l *Level) UnmarshalText(text []byte) error {
	lvl, err := ParseLevel(string(text))
	if err != nil {
		return err
	}
	*l = Level(lvl)
	return nil
}

// AllLevels constant exposing all logging levels.
var AllLevels = []Level{
	PanicLevel,
	FatalLevel,
	ErrorLevel,
	WarnLevel,
	InfoLevel,
	DebugLevel,
}

// These are the different logging levels. You can set the logging level to log
// on your instance of logger, obtained with `logrus.New()`.
const (
	// PanicLevel level, highest level of severity. Logs and then calls panic with the
	// message passed to Debug, Info, ...
	PanicLevel Level = iota
	// FatalLevel level. Logs and then calls `logger.Exit(1)`. It will exit even if the
	// logging level is set to Panic.
	FatalLevel
	// ErrorLevel level. Logs. Used for errors that should definitely be noted.
	// Commonly used for hooks to send errors to an error tracking service.
	ErrorLevel
	// WarnLevel level. Non-critical entries that deserve eyes.
	WarnLevel
	// InfoLevel level. General operational entries about what's going on inside the
	// application.
	InfoLevel
	// DebugLevel level. Usually only enabled when debugging. Very verbose logging.
	DebugLevel
)

// Logger is the base logger used in Atom apps.
type Logger struct {
	entry *logrus.Entry
}

// New creates a new logger, by default this logger uses a simple text
// formatter. The default output will be to os.Stderr. This
// logger will also have all entries appended with the provided
// LogType.
// This logger also supports the writing logs to the file, user has to provide atleast one
// file option from file directory path,file name and file max size.When logger
// is writing logs to file it uses JSONFormatter. Default value of file path is os.Temp(),
// file name is "test.log" and file size is 1024 * 1024 bytes i.e. 1MB.
func New(t LogType, opts ...FileOption) Logger {
	l := Logger{
		entry: &logrus.Entry{
			Logger: &logrus.Logger{
				Out: os.Stderr,
				Formatter: &logrus.TextFormatter{
					DisableColors: true,
					FullTimestamp: true,
				},
				Hooks:        make(logrus.LevelHooks),
				Level:        logrus.InfoLevel,
				ExitFunc:     os.Exit,
				ReportCaller: false,
			},
			Data: logrus.Fields{"type": t},
		},
	}
	l.fileOpts(opts)
	return l
}

// FileConfig contains attributes for file.
type FileConfig struct {
	// FileDirPath is file directory path.
	FileDirPath string
	// FileName is name of file.
	FileName string
	// FileMaxSize is the maximum size of file in bytes.
	FileMaxSize uint64
}

// FileOption enables users to set file attributes.
type FileOption func(*FileConfig)

// WithFileDirPath return a func responsible for setting file directory path
// for FileConfig.
func WithFileDirPath(fileDirPath string) FileOption {
	return func(f *FileConfig) {
		f.FileDirPath = fileDirPath
	}
}

// WithFileName return a func responsible for setting filename for
// FileConfig.
func WithFileName(fileName string) FileOption {
	return func(f *FileConfig) {
		f.FileName = fileName
	}
}

// WithFileMaxSize return a func responsible for setting file max size for
// FileConfig.
func WithFileMaxSize(fileMaxSize uint64) FileOption {
	return func(f *FileConfig) {
		f.FileMaxSize = fileMaxSize
	}
}

// Debug logs a message at level Debug on the standard logger.
func (l Logger) Debug(args ...interface{}) {
	l.entry.Debug(args...)
}

// Debugf logs a message at level Debug on the standard logger.
func (l Logger) Debugf(format string, args ...interface{}) {
	l.entry.Debugf(format, args...)
}

// Info logs a message at level Info on the standard logger.
func (l Logger) Info(args ...interface{}) {
	l.entry.Info(args...)
}

// Infof logs a message at level Info on the standard logger.
func (l Logger) Infof(format string, args ...interface{}) {
	l.entry.Infof(format, args...)
}

// Warn logs a message at level Warn on the standard logger.
func (l Logger) Warn(args ...interface{}) {
	l.entry.Warn(args...)
}

// Warnf logs a message at level Warn on the standard logger.
func (l Logger) Warnf(format string, args ...interface{}) {
	l.entry.Warnf(format, args...)
}

// Error logs a message at level Error on the standard logger.
func (l Logger) Error(args ...interface{}) {
	l.entry.Error(args...)
}

// Errorf logs a message at level Error on the standard logger.
func (l Logger) Errorf(format string, args ...interface{}) {
	l.entry.Errorf(format, args...)
}

// Fatalf is equivalent to Printf() followed by a call to os.Exit(1).
func (l Logger) Fatalf(format string, args ...interface{}) {
	l.entry.Fatalf(format, args...)
}

// WithError creates an entry from the standard logger and adds an error to it, using the value defined in ErrorKey as key.
func (l Logger) WithError(err error) Logger {
	l.entry = l.entry.WithError(err)
	return l
}

// WithField adds additional field to the log message. Field
// "type" cannot be used by users.
// If you need a different type log use log.New(<LogType>).
func (l Logger) WithField(key string, value interface{}) Logger {
	l.entry = l.entry.WithField(key, value)
	return l
}

// WithFields adds additional fields to the log message. Field
// "type" cannot be used by users. Duplicate fields will also
// be removed. If you need a different type log use
// log.New(<LogType>).
func (l Logger) WithFields(fields Fields) Logger {
	if _, ok := fields["type"]; ok {
		l.Errorf("Field \"type\" cannot be assigned by users.  Discarding")
		delete(fields, "type")
	}
	l.entry = l.entry.WithFields(fields)
	return l
}

// SetOutput sets the output to desired io.Writer like stdout, stderr etc
func (l Logger) SetOutput(w io.Writer) {
	l.entry.Logger.Out = w
}

// SetLevel sets the logger level for emitting the log entry.
func (l Logger) SetLevel(level Level) {
	l.entry.Logger.SetLevel(logrus.Level(level))
}

// IsLevelEnabled checks if the log level of the logger is greater than the level param
func (l Logger) IsLevelEnabled(level Level) bool {
	return l.entry.Logger.IsLevelEnabled(logrus.Level(level))
}

func (lt LogType) String() string {
	return string(lt)
}

var defaultLogger = New(AppLog)

// Debug logs a message at level Debug on the standard logger.
func Debug(args ...interface{}) {
	defaultLogger.Debug(args...)
}

// Debugf logs a message at level Debug on the standard logger.
func Debugf(format string, args ...interface{}) {
	defaultLogger.Debugf(format, args...)
}

// Info logs a message at level Info on the standard logger.
func Info(args ...interface{}) {
	defaultLogger.Info(args...)
}

// Infof logs a message at level Info on the standard logger.
func Infof(format string, args ...interface{}) {
	defaultLogger.Infof(format, args...)
}

// Warn logs a message at level Warn on the standard logger.
func Warn(args ...interface{}) {
	defaultLogger.Warn(args...)
}

// Warnf logs a message at level Warn on the standard logger.
func Warnf(format string, args ...interface{}) {
	defaultLogger.Warnf(format, args...)
}

// Error logs a message at level Error on the standard logger.
func Error(args ...interface{}) {
	defaultLogger.Error(args...)
}

// Errorf logs a message at level Error on the standard logger.
func Errorf(format string, args ...interface{}) {
	defaultLogger.Errorf(format, args...)
}

// Fatalf is equivalent to Printf() followed by a call to os.Exit(1).
func Fatalf(format string, args ...interface{}) {
	defaultLogger.Fatalf(format, args...)
}

// WithError creates an entry from the standard logger and adds an error to it
func WithError(err error) Logger {
	return defaultLogger.WithError(err)
}

// WithFields adds additional fields to the log message. Field
// "type" cannot be used by users. Duplicate fields will also
// be removed. If you need a different type log use
// log.New(<LogType>).
func WithFields(fields Fields) Logger {
	return defaultLogger.WithFields(fields)
}

// SetOutput sets the output to desired io.Writer like stdout, stderr etc
func SetOutput(w io.Writer) {
	defaultLogger.SetOutput(w)
}

// SetLevel sets the logger level for emitting the log entry.
func SetLevel(level Level) {
	defaultLogger.SetLevel(level)
}

// IsLevelEnabled checks if the log level of the logger is greater than the level param
func IsLevelEnabled(level Level) bool {
	return defaultLogger.IsLevelEnabled(level)
}

// defaultCfg returns default File config.
func defaultCfg() *FileConfig {
	return &FileConfig{
		FileDirPath: os.TempDir(),
		FileName:    "test",
		FileMaxSize: 1064 * 1064,
	}
}

// fileOpts assigns given file options to file config.
func (l *Logger) fileOpts(opts []FileOption) {
	if len(opts) == 0 {
		return
	}
	cfg := defaultCfg()
	for _, opt := range opts {
		opt(cfg)
	}
	l.entry.Logger.Out = io.Discard
	l.entry.Logger.Hooks.Add(NewHook(
		cfg.FileDirPath,
		cfg.FileName,
		cfg.FileMaxSize,
		&JSONFormatter{},
	))
}

// SetJsonFormatter writes log in JSON format.
func SetJsonFormatter() {
	defaultLogger.entry.Logger.SetFormatter(&logrus.JSONFormatter{})
}

// Set a logger to write logs in JSON format.
func (l Logger) SetJsonFormatter() {
	l.entry.Logger.SetFormatter(&logrus.JSONFormatter{})
}

// WithField adds additional field to the log message. Field
// "type" cannot be used by users.
// If you need a different type log use log.New(<LogType>).
func WithField(key string, value interface{}) Logger {
	return defaultLogger.WithField(key, value)
}
