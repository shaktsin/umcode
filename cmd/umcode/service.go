package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

const launchdLabel = "com.umcode.engine"

var launchdPlist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Bin}}</string>{{if .Config}}
    <string>--config</string><string>{{.Config}}</string>{{end}}
    <string>engine</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>{{.LogDir}}/engine.stdout.log</string>
  <key>StandardErrorPath</key><string>{{.LogDir}}/engine.stderr.log</string>
</dict>
</plist>
`))

var systemdUnit = template.Must(template.New("unit").Parse(`[Unit]
Description=UMCode engine

[Service]
ExecStart={{.Bin}}{{if .Config}} --config {{.Config}}{{end}} engine
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`))

type serviceVars struct {
	Label, Bin, Config, LogDir string
}

func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: umcode service install|uninstall|status")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	bin, _ = filepath.EvalSymlinks(bin)
	home, _ := os.UserHomeDir()
	vars := serviceVars{Label: launchdLabel, Bin: bin, LogDir: cfg.Runtime.LogDir}
	if configPath != "" {
		vars.Config, _ = filepath.Abs(configPath)
	}
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		switch args[0] {
		case "install":
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(cfg.Runtime.LogDir, 0o700); err != nil {
				return err
			}
			f, err := os.Create(plist)
			if err != nil {
				return err
			}
			if err := launchdPlist.Execute(f, vars); err != nil {
				f.Close()
				return err
			}
			f.Close()
			_ = exec.Command("launchctl", "bootout", domain+"/"+launchdLabel).Run()
			if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
				return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
			}
			fmt.Printf("Installed %s; the engine now starts at login.\n", plist)
			return nil
		case "uninstall":
			_ = exec.Command("launchctl", "bootout", domain+"/"+launchdLabel).Run()
			if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("Uninstalled the login service.")
			return nil
		case "status":
			out, err := exec.Command("launchctl", "print", domain+"/"+launchdLabel).CombinedOutput()
			if err != nil {
				fmt.Println("not installed")
				return nil
			}
			for _, l := range strings.Split(string(out), "\n") {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "state =") || strings.HasPrefix(l, "pid =") || strings.HasPrefix(l, "last exit code") {
					fmt.Println(l)
				}
			}
			return nil
		}
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", "umcode.service")
		switch args[0] {
		case "install":
			if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
				return err
			}
			f, err := os.Create(unit)
			if err != nil {
				return err
			}
			if err := systemdUnit.Execute(f, vars); err != nil {
				f.Close()
				return err
			}
			f.Close()
			for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "umcode.service"}} {
				if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
					return fmt.Errorf("systemctl %v: %v: %s", a, err, out)
				}
			}
			fmt.Printf("Installed %s\n", unit)
			return nil
		case "uninstall":
			_ = exec.Command("systemctl", "--user", "disable", "--now", "umcode.service").Run()
			if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("Uninstalled.")
			return nil
		case "status":
			out, _ := exec.Command("systemctl", "--user", "is-active", "umcode.service").CombinedOutput()
			fmt.Print(string(out))
			return nil
		}
	default:
		return fmt.Errorf("service management is not supported on %s", runtime.GOOS)
	}
	return fmt.Errorf("unknown service command %q", args[0])
}
