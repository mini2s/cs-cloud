package cli

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"cs-cloud/internal/app"
)

func logs(a *app.App) error {
	path := a.LogFilePath()
	f, err := openFileShared(path)
	if err != nil {
		printWarn("No log file found")
		printInfo("Path: %s", path)
		return nil
	}
	defer f.Close()

	lines := scanLogLines(f)

	start := len(lines) - 100
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		fmt.Println(line)
	}
	return nil
}

func logf(a *app.App) error {
	tailLogs(a, true)
	return nil
}

func tailLogs(a *app.App, follow bool) {
	path := a.LogFilePath()
	if _, err := os.Stat(path); err != nil {
		printWarn("No log file found")
		return
	}

	f, err := openFileShared(path)
	if err != nil {
		return
	}

	lines := scanLogLines(f)
	f.Close()

	start := len(lines) - 100
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		fmt.Println(line)
	}

	if !follow {
		return
	}

	info, _ := os.Stat(path)
	if info == nil {
		return
	}
	size := info.Size()

	for {
		time.Sleep(500 * time.Millisecond)
		info, err := os.Stat(path)
		if err != nil || info.Size() <= size {
			continue
		}
		f, err := openFileShared(path)
		if err != nil {
			continue
		}
		f.Seek(size, 0)
		scanAndPrintLines(f)
		info2, _ := f.Stat()
		if info2 != nil {
			size = info2.Size()
		}
		f.Close()
	}
}

func scanLogLines(f *os.File) []string {
	var lines []string
	buf := make([]byte, 0, 128*1024)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(buf, 1024*1024)
	scanner.Split(splitLogLines)
	for scanner.Scan() {
		if text := scanner.Text(); text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

func scanAndPrintLines(f *os.File) {
	buf := make([]byte, 0, 128*1024)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(buf, 1024*1024)
	scanner.Split(splitLogLines)
	for scanner.Scan() {
		if text := scanner.Text(); text != "" {
			fmt.Println(text)
		}
	}
}

func splitLogLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	i := indexNewline(data)
	if i >= 0 {
		return i + 1, trimCR(data[:i]), nil
	}
	if atEOF {
		return len(data), trimCR(data), nil
	}
	return 0, nil, nil
}

func trimCR(data []byte) []byte {
	end := len(data)
	for end > 0 && data[end-1] == '\r' {
		end--
	}
	return data[:end]
}

func indexNewline(data []byte) int {
	for i, b := range data {
		if b == '\n' {
			return i
		}
	}
	return -1
}
