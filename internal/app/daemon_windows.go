//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func (a *App) IsProcessRunning(pid int) bool {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
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

func killOrphanProcesses() bool {
	currentPid := os.Getpid()
	cmd := exec.Command("taskkill", "/f", "/fi", fmt.Sprintf("PID ne %d", currentPid), "/im", "cs-cloud.exe")
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	return err == nil
}
