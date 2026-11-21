package tui

import "github.com/iamlakshya/simstim/internal/client"

// decoyNotice is display data only. It never changes the recorded state,
// session identity or whether an agent remains attachable.
func decoyNotice(s client.Session) bool { return !ended(s) && s.Reason == "decoy used" }
