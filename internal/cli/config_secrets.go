package cli

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

// secretDefinition checks the canonical string-or-table union. The shared
// table inspector gives schema findings their original keys and lines.
// The client decoder and normaliser alone decide whether it is usable.
func (c *checker) secretDefinition(raw any, p keyPath) {
	definition, err := client.DecodeSecretDefinition(raw)
	if table, ok := raw.(map[string]any); ok {
		before := len(c.out)
		c.table(table, reflect.TypeFor[client.SecretDefinition](), p)
		if len(c.out) != before {
			return // Keep every unknown, aliased or wrongly typed field.
		}
	}
	if err != nil {
		code := "wrong_type"
		message := ref(p) + " must be a command string or a definition table, not " + valueWords(raw)
		if _, table := raw.(map[string]any); table {
			code, message = "bad_value", client.CleanLine(err.Error())
		}
		c.add(p, finding{Code: code, Message: strings.TrimSuffix(message, ".")})
		return
	}
	name := p[len(p)-1].key // Free-map names keep their exact spelling.
	_, err = client.NormaliseSettingsSecrets(client.Settings{
		Secrets: map[string]client.SecretDefinition{name: definition},
	})
	if err == nil {
		return
	}
	message := client.CleanLine(err.Error())
	if strings.TrimSpace(definition.Run) == "" {
		message = ref(p) + " has an empty command" // Keep the legacy finding.
	}
	c.add(p, finding{Code: "bad_value", Message: strings.TrimSuffix(message, ".")})
}

// checkSecretTiers delegates each entry to the same global settings barrier.
// Reporting entries separately never yields a runnable partial settings view.
func checkSecretTiers(c *checker, raw any, p keyPath) {
	table, _ := raw.(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(table)) {
		tier, ok := table[name].(string)
		if !ok {
			continue // The ordinary map check already reported its type.
		}
		_, err := client.NormaliseSettingsSecrets(client.Settings{
			SecretTiers: map[string]string{name: tier},
		})
		if err != nil {
			c.add(p.add(name), finding{Code: "bad_value", Message: strings.TrimSuffix(client.CleanLine(err.Error()), ".")})
		}
	}
}
