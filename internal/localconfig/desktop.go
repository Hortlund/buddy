package localconfig

import "strings"

func ReadDesktopTheme(path string) string {
	if data, err := readPrivateFile(path); err == nil && strings.TrimSpace(string(data)) == "dark" {
		return "dark"
	}
	return "light"
}

func SaveDesktopTheme(path, name string) error {
	return writePrivateFile(path, []byte(name+"\n"), true)
}
