package cli

import "cs-cloud/internal/app"

func stop(a *app.App) error {
	printInfo("Stopping...")
	stopped := a.StopDaemon()
	if stopped {
		printSuccess("stopped")
	} else {
		printWarn("not running")
	}
	return nil
}
