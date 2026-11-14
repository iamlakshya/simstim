package client

import (
	"io"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func (v *agentSettingsVisitor) streamText(text tomlinspect.Text, writer io.Writer) error {
	chunk := v.stream[:]
	var encoded [4]byte
	used := 0
	flush := func() error {
		if used == 0 {
			return nil
		}
		n, err := writer.Write(chunk[:used])
		if err != nil {
			return err
		}
		if n != used {
			return io.ErrShortWrite
		}
		used = 0
		return nil
	}
	err := text.Walk(func(r rune) error {
		n := utf8.EncodeRune(encoded[:], r)
		if n > len(chunk)-used {
			if err := flush(); err != nil {
				return err
			}
		}
		copy(chunk[used:], encoded[:n])
		used += n
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

func validateAgentSavedHost(value tomlinspect.ValueCursor) error {
	text, err := agentString(value)
	if err != nil {
		return err
	}
	var host hostNameValidation
	err = text.Walk(func(r rune) error {
		host.Rune(r)
		return nil
	})
	if err != nil {
		return err
	}
	if !host.Valid() {
		return secretDefinitionError("a secret has an invalid saved host name in on_hosts")
	}
	return nil
}

func (v *agentSettingsVisitor) secretBinding(fields agentSecretFields, defaults api.SecretBind, selected secretBindingSelection) error {
	// The SAME canonical states are value-owned and reset between scalars.
	// FinishCheck omits the final unselected canonical string. Shared
	// nettarget parsing/normalisation scratch allocations remain unproven.
	v.header.Reset(v.ctx)
	header := &v.header
	if selected.Header == secretBindingLocal {
		text, err := agentString(fields.header)
		if err != nil {
			return err
		}
		if err := v.streamText(text, header); err != nil {
			return err
		}
	} else if selected.Header == secretBindingAgent {
		if _, err := header.Write([]byte(defaults.Header)); err != nil {
			return err
		}
	}
	if err := header.Finish(); err != nil {
		return err
	}
	v.format.Reset(v.ctx)
	format := &v.format
	if selected.DefaultRaw {
		if _, err := format.Write([]byte("{value}")); err != nil {
			return err
		}
	} else if selected.Format == secretBindingLocal {
		text, err := agentString(fields.format)
		if err != nil {
			return err
		}
		if err := v.streamText(text, format); err != nil {
			return err
		}
	} else if selected.Format == secretBindingAgent {
		if _, err := format.Write([]byte(defaults.Format)); err != nil {
			return err
		}
	}
	if err := format.Finish(); err != nil {
		return err
	}
	if selected.Hosts == secretBindingLocal {
		array, err := fields.hosts.Array()
		if err != nil {
			return err
		}
		count, err := array.Len()
		if err != nil {
			return err
		}
		if count == 0 {
			return secretDefinitionError("a secret binding needs an exact HTTPS destination")
		}
		return array.Walk(func(_ uint32, _ tomlinspect.PathCursor, item tomlinspect.ValueCursor) error {
			text, err := agentString(item)
			if err != nil {
				return err
			}
			v.authority.Reset(v.ctx)
			authority := &v.authority
			if err := v.streamText(text, authority); err != nil {
				return err
			}
			return authority.FinishCheck()
		})
	}
	if selected.Hosts != secretBindingAgent || len(defaults.Hosts) == 0 {
		return secretDefinitionError("a secret binding needs an exact HTTPS destination")
	}
	for _, host := range defaults.Hosts {
		v.authority.Reset(v.ctx)
		authority := &v.authority
		if _, err := authority.Write([]byte(host)); err != nil {
			return err
		}
		if err := authority.FinishCheck(); err != nil {
			return err
		}
	}
	return nil
}
