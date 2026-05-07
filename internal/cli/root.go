package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"cs-cloud/internal/app"
	"cs-cloud/internal/platform"
)

func Execute() error {
	parseGlobalFlags()

	a, err := app.New()
	if err != nil {
		return err
	}

	return dispatch(a)
}

func parseGlobalFlags() {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--auth-path" && i+1 < len(args):
			platform.SetAuthPath(args[i+1])
			i++
		case len(args[i]) > 11 && args[i][:11] == "--auth-path=":
			platform.SetAuthPath(args[i][11:])
		case args[i] == "--data-dir" && i+1 < len(args):
			platform.SetDataDir(args[i+1])
			i++
		case len(args[i]) > 11 && args[i][:11] == "--data-dir=":
			platform.SetDataDir(args[i][11:])
		case args[i] == "--no-auto-upgrade":
			platform.SetNoAutoUpgrade(true)
		}
	}
}

func commandArgs() []string {
	var rest []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--auth-path" && i+1 < len(args):
			i++
		case len(args[i]) > 11 && args[i][:11] == "--auth-path=":
		case args[i] == "--data-dir" && i+1 < len(args):
			i++
		case len(args[i]) > 11 && args[i][:11] == "--data-dir=":
		case (args[i] == "--mode" || args[i] == "-m") && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--mode="):
		case strings.HasPrefix(args[i], "-m="):
		case (args[i] == "--port" || args[i] == "-p") && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--port="):
		case strings.HasPrefix(args[i], "-p="):
		case args[i] == "--no-auto-upgrade":
		default:
			rest = append(rest, args[i])
		}
	}
	return rest
}

func parseMode() string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--mode" || args[i] == "-m") && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--mode="):
			return args[i][7:]
		case strings.HasPrefix(args[i], "-m="):
			return args[i][3:]
		}
	}
	return "cloud"
}

func parsePort() (int, error) {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		var raw string
		switch {
		case (args[i] == "--port" || args[i] == "-p") && i+1 < len(args):
			raw = args[i+1]
		case strings.HasPrefix(args[i], "--port="):
			raw = args[i][7:]
		case strings.HasPrefix(args[i], "-p="):
			raw = args[i][3:]
		}
		if raw == "" {
			continue
		}
		port, err := strconv.Atoi(raw)
		if err != nil || port < 0 || port > 65535 {
			return 0, fmt.Errorf("invalid port: %s", raw)
		}
		return port, nil
	}
	return 0, nil
}

func dispatch(a *app.App) error {
	cmds := commandArgs()
	if len(cmds) == 0 {
		printUsage()
		return nil
	}

	switch cmds[0] {
	case "start":
		return start(a)
	case "stop":
		return stop(a)
	case "restart":
		return restart(a)
	case "status":
		return status(a)
	case "logs":
		return logs(a)
	case "logf":
		return logf(a)
	case "doctor":
		return doctor(a)
	case "register":
		return register(a)
	case "login":
		return login(a)
	case "logout":
		return logout(a)
	case "version":
		printVersion()
		return nil
	case "upgrade":
		return upgradeCmd(a)
	case "serve":
		return serve(a)
	case "_daemon":
		return runDaemon(a)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", cmds[0])
	}
}

func printUsage() {
	printTitle("cs-cloud")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud [flags] <command>"))

	printSection("Flags")
	fmt.Print(renderKV([][2]string{
		{"--auth-path", "Path to auth.json (default: ~/.costrict/share/auth.json)"},
		{"--data-dir", "Base data directory (default: ~/.costrict)"},
		{"--mode, -m", "Daemon mode: cloud (default) or local"},
		{"--port, -p", "Local server port (default: random available port)"},
		{"--no-auto-upgrade", "Disable auto-upgrade on cloud command"},
	}))

	printSection("Commands")
	cmds := [][2]string{
		{"version", "Show version info"},
		{"upgrade", "Check and apply upgrades"},
		{"start", "Start daemon (cloud mode with WS tunnel, or --mode local)"},
		{"stop", "Stop daemon"},
		{"restart", "Restart daemon"},
		{"status", "Show daemon status"},
		{"logs", "Show daemon logs"},
		{"logf", "Tail daemon logs (follow mode)"},
		{"doctor", "Show diagnostic info"},
		{"register", "Register device"},
		{"login", "Login via browser OAuth"},
		{"logout", "Delete credentials and device info"},
		{"serve", "Run server in foreground (no daemon)"},
	}
	fmt.Print(renderKV(cmds))
}
