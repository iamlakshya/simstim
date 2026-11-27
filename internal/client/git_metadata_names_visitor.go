package client

import (
	"runtime"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type metadataNameVisitor struct {
	agentSettingsVisitor
	keys      []string
	collision string
}

func (v *metadataNameVisitor) EnterTable(path tomlinspect.PathCursor, table tomlinspect.TableCursor) error {
	// Complete the SAME canonical whole/final schema and unused-definition
	// validation first. Collision checking cannot suppress its diagnostics.
	if err := v.agentSettingsVisitor.EnterTable(path, table); err != nil {
		return err
	}
	definitions, present := v.schema.root.slot("secrets")
	if !present {
		return nil
	}
	secrets, err := definitions.Table()
	if err != nil {
		return err
	}
	return secrets.Walk(func(path tomlinspect.PathCursor, _ tomlinspect.ValueCursor) error {
		if v.collision != "" {
			return nil
		}
		name, err := path.Name()
		if err != nil {
			return err
		}
		for _, key := range v.keys {
			if runtime.GOOS != "windows" {
				equal, err := name.Equal(key)
				if err != nil {
					return err
				}
				if equal {
					v.collision = key
					return nil
				}
				continue
			}
			// Canonical secret names are ASCII. Windows environment names
			// compare without case; no decoded source name is materialised.
			position, equal := 0, true
			err := name.Walk(func(r rune) error {
				if position >= len(key) || asciiMetadataUpper(r) != asciiMetadataUpper(rune(key[position])) {
					equal = false
				}
				position++
				return nil
			})
			if err != nil {
				return err
			}
			if equal && position == len(key) {
				v.collision = key
				return nil
			}
		}
		return nil
	})
}

func asciiMetadataUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}
