package tui

import "github.com/iamlakshya/simstim/internal/client"

func sessionAccountLabelV1(s client.Session) string {
	if s.Alive && s.Wall != nil && s.Wall.Level == "microvm" && s.Wall.OwnAccount {
		return "own account"
	}
	return ""
}
func accountRowFolderV1(s client.Session, where string, width int) string {
	label := sessionAccountLabelV1(s)
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
