package log

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// logName returns a new log file name containing filename and file link name.
func logName(tag string, t time.Time) (name, link string) {
	name = fmt.Sprintf("%s.log.%04d%02d%02d-%02d%02d%02d%010d",
		tag,
		t.Year(),
		t.Month(),
		t.Day(),
		t.Hour(),
		t.Minute(),
		t.Second(),
		t.Nanosecond())
	return name, tag + ".log"
}

// create creates a new log file and returns the file. If the file is created
// successfully, create also attempts to update the symlink for that filename.
func create(logDir string, fileName string, t time.Time) (*os.File, error) {
	err := Validate(logDir)
	if err != nil {
		return nil, err
	}
	name, link := logName(fileName, t)
	fname := filepath.Join(logDir, name)
	f, err := os.Create(fname)
	if err != nil {
		return nil, fmt.Errorf("failed to create file %s at directory %s : %v", name, logDir, err)
	}
	symlink := filepath.Join(logDir, link)
	os.Remove(symlink)
	os.Symlink(name, symlink)
	return f, nil
}

// Validate validates file dirctory path and permission.
func Validate(logDir string) error {
	_, err := os.Stat(logDir)
	if err == nil {
		return nil
	}
	return err
}
