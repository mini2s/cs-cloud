//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (a *App) IsProcessRunning(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func (a *App) StopDaemon() bool {
	pid, err := a.ReadPID()
	if err != nil || !a.IsProcessRunning(pid) {
		a.RemovePID()
		a.RemoveAgentPID()
		return false
	}

	if agentPID, err := a.ReadAgentPID(); err == nil && agentPID > 0 {
		forceKillTree(agentPID)
		a.RemoveAgentPID()
	}

	signalInterruptTree(pid)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !a.IsProcessRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if a.IsProcessRunning(pid) {
		forceKillTree(pid)
	}

	a.RemovePID()
	a.RemoveStopFile()
	return true
}

func signalInterruptTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGINT)
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(os.Interrupt)
}

func forceKillTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}

func forceKillProcess(pid int) {
	forceKillTree(pid)
}

type ConflictingInstance struct {
	PID     int
	CmdLine string
}

func (a *App) FindConflictingInstances() []ConflictingInstance {
	knownPids := map[int]bool{os.Getpid(): true}
	if pid, err := a.ReadPID(); err == nil && pid > 0 {
		knownPids[pid] = true
	}
	if agentPID, err := a.ReadAgentPID(); err == nil && agentPID > 0 {
		knownPids[agentPID] = true
	}

	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	var conflicts []ConflictingInstance
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil || knownPids[pid] {
			continue
		}
		link, err := os.Readlink(filepath.Join("/proc", p.Name(), "exe"))
		if err != nil || link != exe {
			continue
		}
		cmdlineBytes, err := os.ReadFile(filepath.Join("/proc", p.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(cmdlineBytes), "\x00")
		dd := dataDirFromArgs(args)
		if rootDirFromDataDir(dd) != a.rootDir {
			continue
		}
		conflicts = append(conflicts, ConflictingInstance{PID: pid, CmdLine: strings.Join(args, " ")})
	}
	return conflicts
}

func killOrphanProcesses(rootDir string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	killed := false
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		link, err := os.Readlink(filepath.Join("/proc", p.Name(), "exe"))
		if err != nil || link != exe {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", p.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(cmdline), "\x00")
		dd := dataDirFromArgs(args)
		if rootDirFromDataDir(dd) != rootDir {
			continue
		}
		forceKillTree(pid)
		killed = true
	}
	return killed
}
