package cli

import "github.com/iamlakshya/simstim/internal/client"

func sessionAccountMarkV1(s client.Session) string {
	if s.Alive && s.Wall != nil && s.Wall.Level == "microvm" && s.Wall.OwnAccount {
		return " [own account]"
	}
	return ""
}
