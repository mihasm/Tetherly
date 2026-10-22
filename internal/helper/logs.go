package helper

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/mihasm/tetherly/internal/ipc"
)

// LogFile is where the helper writes its log (rotated at 2 MB, one old file kept).
var LogFile = "/Library/Logs/Tetherly/tetherly.log"

const logRotateSize = 2 << 20

// logBuffer keeps the last lines in memory for the `logs` command and copies
// everything to the log file and stderr.
type logBuffer struct {
	mu      sync.Mutex
	lines   []ipc.LogLine
	n       int64
	file    *os.File
	size    int64
	partial bytes.Buffer
}

var logs = &logBuffer{}

// Under launchd stderr goes to a crash log; only a terminal gets the live lines.
var stderrIsTTY = isatty.IsTerminal(os.Stderr.Fd())

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if stderrIsTTY {
		os.Stderr.Write(p)
	}
	b.partial.Write(p)
	for {
		data := b.partial.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := string(data[:i])
		b.partial.Next(i + 1)
		b.n++
		b.lines = append(b.lines, ipc.LogLine{N: b.n, Time: time.Now().Format("15:04:05"), Text: line})
		if len(b.lines) > 2000 {
			b.lines = b.lines[len(b.lines)-2000:]
		}
		b.toFile(line)
	}
	return len(p), nil
}

func (b *logBuffer) toFile(line string) {
	if b.file == nil {
		if err := os.MkdirAll(filepath.Dir(LogFile), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		st, _ := f.Stat()
		b.file, b.size = f, st.Size()
	}
	n, _ := fmt.Fprintf(b.file, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
	b.size += int64(n)
	if b.size > logRotateSize {
		b.file.Close()
		b.file = nil
		_ = os.Rename(LogFile, LogFile+".1")
	}
}

func (b *logBuffer) since(n int64) []ipc.LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []ipc.LogLine
	for _, l := range b.lines {
		if l.N > n {
			out = append(out, l)
		}
	}
	return out
}

// SetupLogging routes zerolog into the buffer as plain text lines.
func SetupLogging(debug bool) {
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(level)
	w := zerolog.ConsoleWriter{Out: logs, NoColor: true, TimeFormat: "15:04:05", PartsExclude: []string{zerolog.TimestampFieldName}}
	w.FormatLevel = func(i interface{}) string {
		if s, ok := i.(string); ok {
			return strings.ToUpper(fmt.Sprintf("%-5s", s))
		}
		return ""
	}
	log.Logger = zerolog.New(w).With().Timestamp().Logger()
}
