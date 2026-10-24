package main

import "golang.org/x/sys/windows/registry"

// taskbarDark reports whether the taskbar uses the dark theme, the Windows 11 default.
func taskbarDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	light, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err != nil || light == 0
}
