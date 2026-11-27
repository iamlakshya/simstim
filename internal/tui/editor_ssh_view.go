package tui

import "github.com/iamlakshya/simstim/internal/client"

func editorSSHLabelV1(session client.Session) string {
	if session.Alive && session.Wall != nil && session.SSH {
		return "ssh"
	}
	return ""
}

func editorSSHRowFolderV1(session client.Session, where string, width int) string {
	label := editorSSHLabelV1(session)
	if label == "" || width <= 0 {
		return where
	}
	if width <= cells(label) {
		return trunc(label, width)
	}
	remaining := width - cells(label) - cells(dot)
	if remaining <= 0 {
		return label
	}
	return truncPath(where, remaining) + dot + label
}
