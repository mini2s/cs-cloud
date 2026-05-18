package provider

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strings"
)

func MachineIDParts() (platform, macAddr, username string) {
	macAddr = getMACAddress()
	username = "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	platform = jsPlatform()
	return
}

func GenerateMachineID() string {
	platform, macAddr, username := MachineIDParts()
	raw := fmt.Sprintf("%s-%s-%s", platform, macAddr, username)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

func GenerateLegacyMachineID() string {
	platform := jsPlatform()
	hostname, _ := os.Hostname()
	username := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	raw := fmt.Sprintf("%s-%s-%s", platform, hostname, username)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

func getMACAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	var addrs []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		addrs = append(addrs, iface.HardwareAddr.String())
	}
	if len(addrs) == 0 {
		return "unknown"
	}
	sort.Strings(addrs)
	return strings.ToLower(strings.ReplaceAll(addrs[0], ":", ""))
}

func JSPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

func jsPlatform() string { return JSPlatform() }

func stripDomain(s string) string {
	if idx := strings.LastIndex(s, `\`); idx >= 0 {
		return s[idx+1:]
	}
	if idx := strings.LastIndex(s, `/`); idx >= 0 {
		return s[idx+1:]
	}
	return s
}
