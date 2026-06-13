package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

// FieldKey is key assigned to particular field.
type FieldKey string

// FieldMap allows customization of the key names for default fields.
type FieldMap map[FieldKey]string

// JSONFormatter formats logs into atom defined standard log event structure.
// i.e Kubernetes log format {"log":"","stream":"","time":"YYYY-MM-DDTHH:MM:SS.XX...Z"}
type JSONFormatter struct {
	// Disabletimestamp logging. useful when timestamp not needed in log message.
	DisableTimestamp bool

	// TimestampFormat to use for display when a full timestamp is printed
	TimestampFormat string

	// FieldMap allows users to customize the names of keys for default fields.
	// As an example:
	// formatter := &JSONFormatter{
	//     FieldMap: FieldMap{
	//         FieldKeyTime:  "@timestamp",
	//         FieldKeyLevel: "@level",
	//         FieldKeyMsg:   "@message"}}
	FieldMap FieldMap
}

// Format implements logrus.Formatter. Format renders a single log entry in Kubernetes log format. e.g.
// {"log":"time=\"2019-09-24T01:12:31+05:30\" level=info msg=\"user transaction succeeded\" task_name=DeleteAction app_name=TestApp23
//
//	   type=AUDIT user_id=1234 user_name=\"John Smith\" task_id=999",
//	 "stream":"",
//	 "time":"2019-09-24T01:12:31+05:30"
//	}
//
// If DisableTimestamp=true then log will be e.g
//
//	{
//	 "log":"level=info msg=\"user transaction succeeded\" TaskName=DeleteAction type=AUDIT appName=TestApp1 userId=1234 userName=\"John Smith\" taskId=12",
//	 "stream":"",
//	 "time":"2019-09-17T14:40:40+05:30"
//	}
//
// If a buffer exists on the entry it will be used else a new buffer will be allocated.
func (f *JSONFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	data := logrus.Fields{}
	for k, v := range entry.Data {
		data[k] = v
	}
	prefixFieldClashes(data, f.FieldMap)
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}

	if !f.DisableTimestamp {
		keys = append(keys, f.FieldMap.resolve(logrus.FieldKeyTime))
	}
	keys = append(keys, f.FieldMap.resolve(logrus.FieldKeyLevel))
	if entry.Message != "" {
		keys = append(keys, f.FieldMap.resolve(logrus.FieldKeyMsg))
	}
	timestampFormat := f.TimestampFormat
	if timestampFormat == "" {
		timestampFormat = time.RFC3339
	}
	buff := &bytes.Buffer{}
	for _, key := range keys {
		switch key {
		case f.FieldMap.resolve(logrus.FieldKeyTime):
			f.appendKeyValue(buff, key, entry.Time.Format(timestampFormat))
		case f.FieldMap.resolve(logrus.FieldKeyLevel):
			f.appendKeyValue(buff, key, entry.Level.String())
		case f.FieldMap.resolve(logrus.FieldKeyMsg):
			f.appendKeyValue(buff, key, entry.Message)
		default:
			f.appendKeyValue(buff, key, data[key])
		}
	}
	logData := logrus.Fields{
		"log":    buff.String(),
		"stream": "",
		"time":   entry.Time.Format(timestampFormat),
	}
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}
	encoder := json.NewEncoder(b)
	if err := encoder.Encode(logData); err != nil {
		return nil, fmt.Errorf("failed to marshal fields to JSON, %v", err)
	}
	return b.Bytes(), nil
}

// resolve returns key value if FieldKey present in FieldMap else return string value of FieldKey.
func (f FieldMap) resolve(key FieldKey) string {
	if k, ok := f[key]; ok {
		return k
	}
	return string(key)
}

// needsQuoting returns true if length of text is zero or text contains special chars apart from
// "-,.,/,@,^,+" . referring this from logrus.
func (f *JSONFormatter) needsQuoting(text string) bool {
	if len(text) == 0 {
		return true
	}
	for _, ch := range text {
		if !((ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '.' || ch == '_' || ch == '/' || ch == '@' || ch == '^' || ch == '+') {
			return true
		}
	}
	return false
}

// appendKeyValue appends a new Key/Value pair to buffer in the format "key=value" where value may be `"`
// delimited if it contains special characters (see needsQuoting). Key/Value pairs will be space separated.
func (f *JSONFormatter) appendKeyValue(b *bytes.Buffer, key string, value interface{}) {
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(key)
	b.WriteByte('=')
	f.appendValue(b, value)
}

// appendValue converts value to string and append it to buffer.
func (f *JSONFormatter) appendValue(b *bytes.Buffer, value interface{}) {
	stringVal, ok := value.(string)
	if !ok {
		stringVal = fmt.Sprint(value)
	}
	if !f.needsQuoting(stringVal) {
		b.WriteString(stringVal)
	} else {
		b.WriteString(fmt.Sprintf("%q", stringVal))
	}
}

// prefixFieldClashes checks for duplicate field key for msg,level,time and
// if present it prefix before key fields.
func prefixFieldClashes(data logrus.Fields, fieldMap FieldMap) {
	timeKey := fieldMap.resolve(logrus.FieldKeyTime)
	if t, ok := data[timeKey]; ok {
		data["fields."+timeKey] = t
		delete(data, timeKey)
	}
	msgKey := fieldMap.resolve(logrus.FieldKeyMsg)
	if m, ok := data[msgKey]; ok {
		data["fields."+msgKey] = m
		delete(data, msgKey)
	}
	levelKey := fieldMap.resolve(logrus.FieldKeyLevel)
	if l, ok := data[levelKey]; ok {
		data["fields."+levelKey] = l
		delete(data, levelKey)
	}
}
