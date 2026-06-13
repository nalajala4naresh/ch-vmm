package log

import (
	"bufio"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// FileHook is a logrus hook to handle writing logs to given file.
type FileHook struct {
	FilePath    string
	FileName    string
	FileMaxSize uint64
	levels      []logrus.Level

	mu        sync.Mutex
	formatter logrus.Formatter
	w         *SyncBuffer // file writer.
}

// NewHook returns new File hook.
func NewHook(filepath string, filename string, fileMaxSize uint64, formatter logrus.Formatter) *FileHook {
	return &FileHook{
		formatter:   formatter,
		FileName:    filename,
		FilePath:    filepath,
		FileMaxSize: fileMaxSize,
	}
}

// Fire writes the log file to defined path.
// User who run this function needs write permissions to the file or directory.
func (f *FileHook) Fire(entry *logrus.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.write(entry); err != nil {
		Errorf("failed to write log to file : %v", err)
		// exiting if failed to write log to file
		os.Exit(1)
	}
	return nil
}

// Levels returns configured log levels.
func (f *FileHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

// write a log line to file.
// write requires f.mu to be held.
func (f *FileHook) write(entry *logrus.Entry) error {
	msg, err := f.formatter.Format(entry)
	if err != nil {
		return err
	}
	if f.w == nil {
		if err := f.createSyncBuffer(f.FilePath, f.FileName, f.FileMaxSize); err != nil {
			return err
		}
	}
	if _, err = f.w.Write(msg); err != nil {
		return err
	}
	return nil
}

// SyncBuffer joins a bufio.Writer to its underlying file, providing access to the
// file's Sync method and providing a wrapper for the Write method that provides log
// file rotation.
type SyncBuffer struct {
	*bufio.Writer
	file     *os.File
	logDir   string
	fileName string
	fileSize uint64
	// nbytes the number of bytes written to this file
	nbytes uint64
}

// Write writes the log line to bufio.Writer.
// It rotates the file if file size is greater than max file size given.
func (sb *SyncBuffer) Write(p []byte) (n int, err error) {
	if sb.nbytes+uint64(len(p)) >= sb.fileSize {
		if err := sb.rotateFile(time.Now()); err != nil {
			return 0, err
		}
	}
	n, err = sb.Writer.Write(p)
	if err != nil {
		return 0, err
	}
	sb.nbytes += uint64(n)
	sb.Flush()
	return int(sb.nbytes), nil
}

// rotateFile closes the syncBuffer's file and starts a new one.
func (sb *SyncBuffer) rotateFile(now time.Time) error {
	if sb.file != nil {
		sb.Flush()
		sb.file.Close()
	}
	var err error
	sb.file, err = create(sb.logDir, sb.fileName, now)
	if err != nil {
		return err
	}
	// reset the number of bytes written to this file to 0.
	sb.nbytes = 0
	// create bufio writer with byte size 256kb.
	sb.Writer = bufio.NewWriterSize(sb.file, 1024*256)
	return nil
}

// createSyncBuffer sync buffer for file.
func (f *FileHook) createSyncBuffer(logDir string, fileName string, fileMaxSize uint64) error {
	now := time.Now()
	sb := &SyncBuffer{
		logDir:   logDir,
		fileName: fileName,
		fileSize: fileMaxSize,
	}
	if err := sb.rotateFile(now); err != nil {
		return err
	}
	f.w = sb
	return nil
}
