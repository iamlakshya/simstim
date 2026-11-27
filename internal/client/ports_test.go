package client

import "testing"

func TestForwardSocketCannotChangePrimaryPortOrSSHGrammar(t *testing.T) {
	const primary = "port2345"
	if !validPortSocket("/run/user/1000/tool/p/port2345/3000.sock", primary, 3000) {
		t.Fatal("ordinary exact socket refused")
	}
	for _, socket := range []string{
		"relative/p/port2345/3000.sock", "/run/x/p/other234/3000.sock", "/run/x/p/port2345/3001.sock",
		"/run/x/../p/port2345/3000.sock", "/run/x:host/p/port2345/3000.sock", "/run/x%h/p/port2345/3000.sock", "/run/x\n/p/port2345/3000.sock",
	} {
		if validPortSocket(socket, primary, 3000) {
			t.Fatalf("foreign or unsafe socket passed: %q", socket)
		}
	}
}
