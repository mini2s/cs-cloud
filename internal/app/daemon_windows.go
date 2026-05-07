//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (a *App) IsProcessRunning(pid int) bool {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	err = syscall.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}
	return exitCode == 259
}

func (a *App) StopDaemon() bool {
	pid, err := a.ReadPID()
	if err != nil || !a.IsProcessRunning(pid) {
		a.RemovePID()
		a.RemoveAgentPID()
		return false
	}

	if agentPID, err := a.ReadAgentPID(); err == nil && agentPID > 0 {
		forceKill(agentPID)
		a.RemoveAgentPID()
	}

	os.WriteFile(a.stopFile(), []byte(fmt.Sprintf("%d", time.Now().UnixMilli())), 0o644)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !a.IsProcessRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if a.IsProcessRunning(pid) {
		forceKill(pid)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !a.IsProcessRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	a.RemovePID()
	a.RemoveStopFile()
	return true
}

func forceKill(pid int) {
	cmd := exec.Command("taskkill", "/pid", fmt.Sprintf("%d", pid), "/f", "/t")
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

func forceKillProcess(pid int) {
	forceKill(pid)
}

func killOrphanProcesses(rootDir string) bool {
	currentPid := os.Getpid()
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf(
			"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+
				"Get-CimInstance Win32_Process -Filter \"name='cs-cloud.exe' AND ProcessId<>%d\" | "+
				"ForEach-Object { $_.ProcessId.ToString() + '|' + $_.CommandLine }",
			currentPid))
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	killed := false
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sep := strings.IndexByte(line, '|')
		if sep < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sep])
		if err != nil {
			continue
		}
		cmdline := line[sep+1:]
		dd := extractDataDirFromCmdLine(cmdline)
		if rootDirFromDataDir(dd) != rootDir {
			continue
		}
		forceKill(pid)
		killed = true
	}
	return killed
}

func extractDataDirFromCmdLine(cmdline string) string {
	for _, prefix := range []string{"--data-dir=", "--data-dir "} {
		idx := strings.Index(cmdline, prefix)
		if idx < 0 {
			continue
		}
		val := cmdline[idx+len(prefix):]
		val = strings.TrimLeft(val, " \t")
		if len(val) > 0 && val[0] == '"' {
			val = val[1:]
			if end := strings.IndexByte(val, '"'); end >= 0 {
				return val[:end]
			}
			return val
		}
		if end := strings.IndexAny(val, " \t\r\n"); end >= 0 {
			return val[:end]
		}
		return val
	}
	return ""
}
