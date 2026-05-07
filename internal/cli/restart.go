package cli

import "cs-cloud/internal/app"

func restart(a *app.App) error {
	running, _ := a.DaemonStatus()
	if running {
		printInfo("Stopping previous instance...")
		stopped := a.StopDaemon()
		if stopped {
			printInfo("Stopped previous instance")
		}
	} else if cleaned := a.ForceCleanupStale(); cleaned {
		printInfo("Cleaned up stale daemon process")
	}
	return start(a)
}
