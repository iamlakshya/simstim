package tui

import "github.com/iamlakshya/simstim/internal/client"

func liveSavedImageLabel(host *hostConn, session client.Session) string {
	if host == nil || !host.loaded || !host.online() || host.idChanged || host.conn.HostIDChanged() {
		return ""
	}
	if label := client.SavedImageLabel(session, host.name(), host.conn.HostID()); label != "" {
		return "image " + label
	}
	return ""
}
