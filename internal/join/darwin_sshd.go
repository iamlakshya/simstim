//go:build darwin

package join

import (
	"context"
	"encoding/xml"
	"io"
	"strings"
)

// macReloadSSHD verifies Apple's immutable inetd-style launchd service. New
// connections get a new sshd and read the checked drop-in themselves; HUP would
// target an unrelated live connection. Remote Login is never enabled here.
func macReloadSSHD(ctx context.Context) error {
	data, _, err := macReadFile("/System/Library/LaunchDaemons/ssh.plist", 1<<20)
	if err != nil {
		return err
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	key := ""
	program := ""
	inetd := false
	sockets := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return err
			}
			inetd = inetd || key == "inetdCompatibility"
			sockets = sockets || key == "Sockets"
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return err
			}
			if key == "Program" {
				program = value
			}
		}
	}
	if program != "/usr/libexec/sshd-keygen-wrapper" || !inetd || !sockets {
		return macFailure("join_mac_sshd", "the Mac has another sshd activation service; ask its owner or MDM administrator to reload and verify it")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = macCommand(ctx, nil, nil, "/usr/sbin/sshd", "-t")
	return err
}
