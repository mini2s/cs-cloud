package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/version"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

type Downloader struct {
	httpClient *http.Client
	tmpDir     string
}

func NewDownloader(tmpDir string) *Downloader {
	return &Downloader{
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		tmpDir: tmpDir,
	}
}

func (d *Downloader) Download(ctx context.Context, url, expectedSHA256 string) (string, error) {
	if err := os.MkdirAll(d.tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create tmp dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(d.tmpDir, "cs-cloud-update-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		if _, err := os.Stat(tmpPath); err == nil {
			os.Remove(tmpPath)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed (status %d)", resp.StatusCode)
	}

	contentLength := resp.ContentLength
	hasher := sha256.New()
	w := io.MultiWriter(tmpFile, hasher)

	if contentLength > 0 {
		pw := &loggingProgressWriter{src: resp.Body, total: contentLength}
		if _, err := io.CopyBuffer(w, pw, make([]byte, 32*1024)); err != nil {
			return "", fmt.Errorf("write download: %w", err)
		}
	} else {
		if _, err := io.Copy(w, resp.Body); err != nil {
			return "", fmt.Errorf("write download: %w", err)
		}
	}

	if err := tmpFile.Sync(); err != nil {
		return "", fmt.Errorf("sync download: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close download: %w", err)
	}

	actual := fmt.Sprintf("%x", hasher.Sum(nil))
	if expectedSHA256 != "" && actual != expectedSHA256 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedSHA256, actual)
	}

	binaryPath, err := d.extractBinary(tmpPath, url)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("extract binary: %w", err)
	}

	if runtime.GOOS == "darwin" {
		if err := removeQuarantine(binaryPath); err != nil {
			logger.Warn("terminal: failed to remove quarantine: %v", err)
		}
	}

	return binaryPath, nil
}

type archiveFormat int

const (
	archiveUnknown archiveFormat = iota
	archiveTarGz
	archiveZip
	archiveRawBinary
)

func detectArchiveFormat(archivePath, downloadURL string) archiveFormat {
	lower := strings.ToLower(downloadURL)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return archiveTarGz
	case strings.HasSuffix(lower, ".zip"):
		return archiveZip
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return archiveRawBinary
	}
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil || n < 4 {
		return archiveRawBinary
	}

	if buf[0] == 0x1f && buf[1] == 0x8b {
		return archiveTarGz
	}
	if buf[0] == 'P' && buf[1] == 'K' && buf[2] == 0x03 && buf[3] == 0x04 {
		return archiveZip
	}

	return archiveRawBinary
}

func removeQuarantine(path string) error {
	_, err := exec.LookPath("xattr")
	if err != nil {
		return fmt.Errorf("xattr not found: %w", err)
	}
	out, err := exec.Command("xattr", "-d", "com.apple.quarantine", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xattr -d quarantine: %s: %w", string(out), err)
	}
	logger.Info("quarantine attribute removed: %s", path)
	return nil
}

func (d *Downloader) extractBinary(archivePath, downloadURL string) (string, error) {
	af := detectArchiveFormat(archivePath, downloadURL)

	var binaryPath string
	var err error

	switch af {
	case archiveTarGz:
		binaryPath, err = d.extractFromTarGz(archivePath)
	case archiveZip:
		binaryPath, err = d.extractFromZip(archivePath)
	default:
		dst := filepath.Join(d.tmpDir, "cs-cloud-new")
		if err := os.Rename(archivePath, dst); err != nil {
			return "", fmt.Errorf("rename temp file: %w", err)
		}
		if err := os.Chmod(dst, 0o755); err != nil {
			os.Remove(dst)
			return "", fmt.Errorf("chmod binary: %w", err)
		}
		return dst, nil
	}

	if err != nil {
		return "", err
	}

	if err := os.Chmod(binaryPath, 0o755); err != nil {
		os.Remove(binaryPath)
		return "", fmt.Errorf("chmod binary: %w", err)
	}

	return binaryPath, nil
}

func (d *Downloader) extractFromTarGz(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar read: %w", err)
		}

		name := filepath.Base(hdr.Name)
		if name != "cs-cloud" && name != "cs-cloud.exe" {
			continue
		}

		dst := filepath.Join(d.tmpDir, "cs-cloud-new")
		out, err := os.Create(dst)
		if err != nil {
			return "", fmt.Errorf("create extracted file: %w", err)
		}

		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(dst)
			return "", fmt.Errorf("extract file: %w", err)
		}
		out.Close()

		return dst, nil
	}

	return "", fmt.Errorf("cs-cloud binary not found in tar.gz archive")
}

func (d *Downloader) extractFromZip(archivePath string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if name != "cs-cloud" && name != "cs-cloud.exe" {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open zip entry: %w", err)
		}

		dst := filepath.Join(d.tmpDir, "cs-cloud-new")
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("create extracted file: %w", err)
		}

		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			os.Remove(dst)
			return "", fmt.Errorf("extract file: %w", err)
		}
		out.Close()
		rc.Close()

		return dst, nil
	}

	return "", fmt.Errorf("cs-cloud binary not found in zip archive")
}

type loggingProgressWriter struct {
	src      io.Reader
	total    int64
	wrote    int64
	lastLog  time.Time
}

func (pw *loggingProgressWriter) Read(b []byte) (int, error) {
	n, err := pw.src.Read(b)
	pw.wrote += int64(n)
	now := time.Now()
	if pw.wrote == pw.total || now.Sub(pw.lastLog) >= 5*time.Second {
		pw.lastLog = now
		pct := float64(pw.wrote) * 100 / float64(pw.total)
		logger.Info("download progress: %.1f%% (%d/%d bytes)", pct, pw.wrote, pw.total)
	}
	return n, err
}

type progressWriter struct {
	w     io.Reader
	p     *downloadProgress
	total int64
	wrote int64
}

func (pw *progressWriter) Read(b []byte) (int, error) {
	n, err := pw.w.Read(b)
	pw.wrote += int64(n)
	pw.p.setProgress(float64(pw.wrote) / float64(pw.total))
	return n, err
}

type downloadProgress struct {
	prog     progress.Model
	pct      float64
	finished bool
	program  *tea.Program
}

func newDownloadProgress(total int64) *downloadProgress {
	prog := progress.New(progress.WithGradient("#7D56F4", "#04B575"), progress.WithWidth(40))
	dp := &downloadProgress{prog: prog}
	dp.program = tea.NewProgram(dp)
	go dp.program.Run()
	return dp
}

func (dp *downloadProgress) setProgress(pct float64) {
	dp.pct = pct
	dp.program.Send(progressMsg(pct))
}

func (dp *downloadProgress) finish() {
	dp.finished = true
	dp.program.Send(progressMsg(1.0))
	time.Sleep(300 * time.Millisecond)
	dp.program.Quit()
}

func (dp *downloadProgress) quit() {
	dp.program.Quit()
}

type progressMsg float64

func (dp downloadProgress) Init() tea.Cmd {
	return nil
}

func (dp downloadProgress) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case progressMsg:
		dp.pct = float64(msg)
		if dp.pct >= 1.0 {
			return dp, tea.Quit
		}
		return dp, nil
	case tea.WindowSizeMsg:
		dp.prog.Width = msg.Width - 20
		if dp.prog.Width < 10 {
			dp.prog.Width = 10
		}
		return dp, nil
	}
	return dp, nil
}

func (dp downloadProgress) View() string {
	bar := dp.prog.ViewAs(dp.pct)
	pct := fmt.Sprintf("%5.1f%%", dp.pct*100)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#B0B0B0"))
	if dp.finished {
		return labelStyle.Render("  Downloading") + " " + bar + " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("done")
	}
	return labelStyle.Render("  Downloading") + " " + bar + " " + pct
}
