// Package macfacts holds bounded advisory facts from a joined Mac's fixed
// root-owned handler. Facts never grant setup, execution or capacity.
package macfacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// Version and MaxBytes bound the fixed outer-hop response.
const Version = 1
const MaxBytes = 32 << 10

// Facts describes only public host observations. Every Known bit distinguishes
// unavailable evidence from a healthy zero. No raw command output is retained.
type Facts struct {
	Version              int    `json:"v"`
	Kind                 string `json:"kind"`
	MacOS                string `json:"macos"`
	Chip                 string `json:"chip"`
	OnBattery            bool   `json:"on_battery"`
	OnBatteryKnown       bool   `json:"on_battery_known"`
	BatteryPercent       int    `json:"battery_percent"`
	BatteryKnown         bool   `json:"battery_known"`
	ACSleepMinutes       int    `json:"ac_sleep_minutes"`
	SleepPrevented       bool   `json:"sleep_prevented"`
	SleepAssertionsKnown bool   `json:"sleep_assertions_known"`
	ACSleepKnown         bool   `json:"ac_sleep_known"`
	MDMEnrolled          bool   `json:"mdm_enrolled"`
	MDMKnown             bool   `json:"mdm_known"`
	SSHDManaged          bool   `json:"sshd_managed"`
	SSHDManagedKnown     bool   `json:"sshd_managed_known"`
	ThermalWarning       bool   `json:"thermal_warning"`
	ThermalKnown         bool   `json:"thermal_known"`
	StartMode            string `json:"start_mode"`
	LaunchEnabled        bool   `json:"launch_enabled"`
	LaunchKnown          bool   `json:"launch_known"`
}

// Decode checks one closed response without turning it into source authority.
func Decode(data []byte) (Facts, error) {
	var facts Facts
	if len(data) == 0 || len(data) > MaxBytes {
		return facts, fmt.Errorf("the Mac facts response exceeds its bound")
	}
	if err := canonicalFactsFields(data); err != nil {
		return Facts{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&facts); err != nil {
		return Facts{}, fmt.Errorf("decode Mac facts: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || facts.Version != Version || facts.Kind != "mac" ||
		!clean(facts.MacOS) || !clean(facts.Chip) || facts.BatteryPercent < 0 || facts.BatteryPercent > 100 ||
		facts.ACSleepMinutes < 0 || facts.ACSleepMinutes > 1440 ||
		facts.StartMode != "daemon" && facts.StartMode != "user-agent" && facts.StartMode != "" {
		return Facts{}, fmt.Errorf("the Mac facts response is invalid or unsupported")
	}
	return facts, nil
}

func clean(value string) bool {
	if len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// ParseBattery reads pmset's documented power-source and percentage output.
func ParseBattery(text string) (battery, powerKnown bool, percent int, percentKnown bool) {
	first, _, _ := strings.Cut(text, "\n")
	switch strings.TrimSpace(first) {
	case "Now drawing from 'Battery Power'":
		battery, powerKnown = true, true
	case "Now drawing from 'AC Power'":
		powerKnown = true
	}
	for _, field := range strings.Fields(text) {
		value, found := strings.CutSuffix(strings.TrimSuffix(field, ";"), "%")
		if !found {
			continue
		}
		n, err := strconv.Atoi(value)
		if err == nil && n >= 0 && n <= 100 && !percentKnown {
			percent, percentKnown = n, true
		} else {
			return battery, powerKnown, 0, false
		}
	}
	return battery, powerKnown, percent, percentKnown
}

// ParseACSleep reads only the AC section; battery values never become undo.
func ParseACSleep(text string) (int, bool) {
	inAC := false
	found := false
	value := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ":") {
			inAC = line == "AC Power:"
			continue
		}
		fields := strings.Fields(line)
		if !inAC || len(fields) < 2 || fields[0] != "sleep" {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if found || err != nil || n < 0 || n > 1440 {
			return 0, false
		}
		found, value = true, n
	}
	return value, found
}

// ParseMDM recognises enrollment status without retaining organisation data.
func ParseMDM(text string) (enrolled, known bool) {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "MDM enrollment" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "No" {
			return false, true
		}
		if value == "Yes" || strings.HasPrefix(value, "Yes (") {
			return true, true
		}
	}
	return false, false
}

// SupportedMacOS checks the native version, not the Linux guest's kernel.
func SupportedMacOS(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 15
}

// ParseSleepAssertions keeps only the two system-sleep counters, never names
// of apps, processes or assertion descriptions.
func ParseSleepAssertions(text string) (prevented, known bool) {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "PreventSystemSleep" && fields[0] != "PreventUserIdleSystemSleep" {
			continue
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil || value < 0 {
			return false, false
		}
		count++
		prevented = prevented || value > 0
	}
	return prevented, count == 2
}

// ParseThermal recognises observed warning/limit values without guessing
// that a successful command means healthy hardware.
func ParseThermal(text string) (warning, known bool) {
	if strings.Contains(text, "No thermal warning level has been recorded") {
		return false, true
	}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "CPU_Speed_Limit":
			if number >= 0 && number <= 100 {
				return number < 100, true
			}
		case "Thermal_Level":
			if number >= 0 {
				return number > 0, true
			}
		}
	}
	return false, false
}

func canonicalFactsFields(data []byte) error {
	allowed := map[string]bool{}
	shape := reflect.TypeFor[Facts]()
	for i := 0; i < shape.NumField(); i++ {
		allowed[shape.Field(i).Tag.Get("json")] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("the Mac facts response is not an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read Mac facts field: %w", err)
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return fmt.Errorf("the Mac facts response has an unknown, aliased or duplicate field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("read Mac facts value: %w", err)
		}
	}
	return nil
}
