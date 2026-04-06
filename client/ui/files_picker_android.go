//go:build android

package ui

import "fmt"

// openFilePicker is a stub on Android.
// The Android file picker is invoked via Intent from the Java layer.
// Users can also type/paste the file path manually.
func openFilePicker() (string, error) {
	return "", fmt.Errorf("use the file path input or Android share intent")
}
