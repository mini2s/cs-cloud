package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
)

func status(a *app.App) error {
	running, pid, reason := a.DaemonStatus()
	cred, err := provider.LoadCredentials()
	if err != nil {
		return err
	}
	dev, err := a.Device()
	if err != nil {
		return err
	}
	mode := a.LoadMode()
	serverURL, err := a.ServerURL()
	if err != nil {
		return err
	}

	printTitle("cs-cloud status")

	deviceIDVal := ""
	if dev != nil {
		deviceIDVal = dev.DeviceID
	} else {
		deviceIDVal = provider.GenerateMachineID()
	}

	if running {
		printSuccess("Running")
		printSection("Developer info")
		printKV("pid", fmt.Sprintf("%d", pid))
		printKV("mode", mode)
		printKV("root", a.RootDir())
		printKV("cloud_url", a.CloudBaseURL())
		printKV("auth", fmt.Sprintf("%t", cred != nil))
		if cred != nil {
			if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
				user := claims.ResolveDisplayName()
				p := claims.ResolveProvider()
				if p != "" || user != "" {
					printKV("user", p+"/"+user)
				}
			}
		}
		printKV("device", fmt.Sprintf("%t", dev != nil))
		printKV("device_id", deviceIDVal)
		p, h, u := provider.MachineIDParts()
		printKV("device_id.platform", p)
		printKV("device_id.hostname", h)
		printKV("device_id.username", u)
		printKV("local_url", serverURL)
		printKV("logs", filepath.Join(a.RootDir(), "app.log"))

		if mode == "cloud" {
			webURL := strings.TrimSuffix(a.CloudBaseURL(), "/cloud-api") + "/cloud"
			fmt.Println()
			fmt.Println(headingStyle.Render("→ Cloud dashboard"))
			fmt.Printf("  %s\n", valueStyle.Render(webURL))
		}
	} else {
		if reason != "" {
			printWarn("Stopped (%s)", reason)
		} else {
			printInfo("Stopped")
		}
		printSection("Developer info")
		printKV("root", a.RootDir())
		printKV("cloud_url", a.CloudBaseURL())
		printKV("auth", fmt.Sprintf("%t", cred != nil))
		if cred != nil {
			if claims, err := provider.ParseJWT(cred.AccessToken); err == nil {
				user := claims.ResolveDisplayName()
				p := claims.ResolveProvider()
				if p != "" || user != "" {
					printKV("user", p+"/"+user)
				}
			}
		}
		printKV("device", fmt.Sprintf("%t", dev != nil))
		printKV("device_id", deviceIDVal)
	}
	return nil
}
