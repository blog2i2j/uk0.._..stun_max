//go:build !android

package ui

import "github.com/sqweek/dialog"

// openFilePicker opens the native OS file dialog on desktop platforms.
func openFilePicker() (string, error) {
	return dialog.File().Title("Select file to send").Load()
}
