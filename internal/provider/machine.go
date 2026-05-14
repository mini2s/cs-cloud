package provider

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
)

func MachineIDParts() (platform, hostname, username string) {
	hostname, _ = os.Hostname()
	username = "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = stripDomain(u.Username)
	}
	platform = jsPlatform()
	return
}

func GenerateMachineID() string {
	platform, hostname, username := MachineIDParts()
	raw := fmt.Sprintf("%s-%s-%s", platform, hostname, username)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
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
