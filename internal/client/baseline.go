package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

type baselineReadingV1 struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	SHA256 string `json:"sha256,omitempty"`
}
type baselineFileV1 struct {
	Version  int                 `json:"v"`
	Host     string              `json:"host"`
	HostID   string              `json:"host_id"`
	At       time.Time           `json:"at"`
	Readings []baselineReadingV1 `json:"readings"`
}

var baselineKeysV1 = []string{"bashrc", "profile", "bash_profile", "zshrc", "fish_config", "authorized_keys", "ssh_rc", "systemd_user", "crontab"}
var baselineLabelsV1 = map[string]string{"bashrc": "~/.bashrc", "profile": "~/.profile", "bash_profile": "~/.bash_profile", "zshrc": "~/.zshrc", "fish_config": "~/.config/fish/config.fish", "authorized_keys": "~/.ssh/authorized_keys", "ssh_rc": "~/.ssh/rc", "systemd_user": "~/.config/systemd/user/", "crontab": "crontab"}

// BaselineCapture is a read-only advisory observation of the original add
// action. It supplies no parent-directory, file-removal or execution grant.
type BaselineCapture struct {
	host        Host
	observation *HostStateObservation
	at          time.Time
	readings    []baselineReadingV1
}

// CaptureHostBaseline runs the fixed read-only command before helper setup.
// An add's existing registration observation is retained rather than replaced.
func CaptureHostBaseline(ctx context.Context, h Host, transport Transport) (*BaselineCapture, error) {
	if ctx == nil || transport == nil || !ValidHostName(h.Name) {
		return nil, baselineErrorV1("the baseline has no original host action")
	}
	observation, err := captureHostStateObservation(ctx, h)
	if err != nil {
		return nil, err
	}
	if observation.role != hostStateRegistration {
		return nil, baselineErrorV1("the first baseline must belong to the original host registration")
	}
	attempt, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt)
	if err := checkHostTransportAttempt(ctx, attempt, h); err != nil {
		return nil, err
	}
	if !attempt.matchesRegistrationTransportV1(transport) {
		return nil, baselineErrorV1("the baseline has no exact actual registration transport")
	}
	output, err := captureRegistrationBaselineNativeV1(ctx, h, transport, observation)
	if err != nil {
		return nil, err
	}
	readings, err := parseBaselineV1(output)
	if err != nil {
		return nil, err
	}
	if err := requireHostStatePayloadObservation(ctx, h, observation); err != nil {
		return nil, err
	}
	return &BaselineCapture{host: cloneObservationHost(h), observation: observation, at: time.Now().UTC(), readings: readings}, nil
}

// RecordHostBaseline saves only under the SAME actual registration owner.
// Neither the captured DTO nor a later saved host can open another source.
func (c *Conn) RecordHostBaseline(ctx context.Context, capture *BaselineCapture) error {
	if capture == nil || c == nil || !SameEntry(c.Host, capture.host) || !ValidHostID(c.HostID()) {
		return baselineErrorV1("the baseline lost its original registration connection")
	}
	if err := requireHostStatePayloadObservation(ctx, c.Host, capture.observation); err != nil {
		return err
	}
	if err := c.BindHostStateObservation(ctx); err != nil {
		return err
	}
	record := baselineFileV1{Version: 1, Host: c.Host.Name, HostID: c.HostID(), At: capture.at, Readings: append([]baselineReadingV1(nil), capture.readings...)}
	return withStateFile(ctx, []string{"hosts", HostFileKey(c.Host.Name)}, "baseline.json", true, func(raw []byte, exists bool) ([]byte, error) {
		if exists {
			if _, err := decodeBaselineV1(raw, c.Host.Name, c.HostID()); err != nil {
				return nil, err
			}
			return raw, nil
		}
		return json.Marshal(record)
	})
}

func parseBaselineV1(output []byte) ([]baselineReadingV1, error) {
	if len(output) > 16<<10 {
		return nil, baselineErrorV1("the read-only baseline exceeded its fixed bound")
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	if !scanner.Scan() || scanner.Text() != "baseline-v1" {
		return nil, baselineErrorV1("the host returned an unknown baseline format")
	}
	var readings []baselineReadingV1
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 || len(readings) >= len(baselineKeysV1) || fields[0] != baselineKeysV1[len(readings)] {
			return nil, baselineErrorV1("the host baseline has missing, repeated or unexpected rows")
		}
		row := baselineReadingV1{Key: fields[0], State: fields[1], SHA256: fields[2]}
		if !validBaselineReadingV1(row) {
			return nil, baselineErrorV1("the host baseline contains an unknown reading")
		}
		readings = append(readings, row)
	}
	if scanner.Err() != nil || len(readings) != len(baselineKeysV1) {
		return nil, baselineErrorV1("the host baseline was incomplete")
	}
	return readings, nil
}

func validBaselineReadingV1(row baselineReadingV1) bool {
	return row.State == "known" && supply.IsHex64(row.SHA256) || (row.State == "absent" || row.State == "unknown") && row.SHA256 == ""
}

func decodeBaselineV1(raw []byte, host, hostID string) (baselineFileV1, error) {
	var record baselineFileV1
	if !api.ExactJSONFields(raw, record) || json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.Host != host || record.HostID != hostID || !ValidHostID(hostID) || record.At.IsZero() || len(record.Readings) != len(baselineKeysV1) {
		return record, baselineErrorV1("the saved baseline belongs to another host or schema")
	}
	for index, row := range record.Readings {
		if row.Key != baselineKeysV1[index] || !validBaselineReadingV1(row) {
			return record, baselineErrorV1("the saved baseline has an unknown reading")
		}
	}
	return record, nil
}

type BaselineChanges struct {
	Changed     []string
	Unavailable []string
}

func compareBaselineV1(before, after []baselineReadingV1) BaselineChanges {
	var result BaselineChanges
	if len(before) != len(baselineKeysV1) || len(after) != len(before) {
		result.Unavailable = append(result.Unavailable, "baseline")
		return result
	}
	for index, old := range before {
		current := after[index]
		if old.Key != baselineKeysV1[index] || current.Key != old.Key || !validBaselineReadingV1(old) || !validBaselineReadingV1(current) || old.State == "unknown" || current.State == "unknown" {
			result.Unavailable = append(result.Unavailable, baselineLabelsV1[baselineKeysV1[index]])
			continue
		}
		if old.State != current.State || old.SHA256 != current.SHA256 {
			result.Changed = append(result.Changed, baselineLabelsV1[old.Key])
		}
	}
	return result
}

func baselineErrorV1(message string) error {
	return &UserError{Msg: message, Hint: "Keep the original host baseline and inspect host doctor before retrying removal.", Code: "baseline", Kind: KindRefused}
}

func baselineChangeTextV1(changes BaselineChanges) string {
	return fmt.Sprintf("Changed since the host was added: %s", strings.Join(changes.Changed, ", "))
}

func captureRegistrationBaselineNativeV1(ctx context.Context, h Host, transport Transport, observation *HostStateObservation) (raw []byte, returned error) {
	native, finish := ctx, func() error { return nil }
	if ssh, ok := transport.(*SSH); ok {
		if !SameEntry(ssh.entryHost(), h) {
			return nil, baselineErrorV1("the native registration route names another host")
		}
		var err error
		native, finish, err = ssh.beginNativeSource(ctx)
		if err != nil {
			return nil, err
		}
		if err := ssh.checkNativeSourceScope(native); err != nil {
			return nil, errors.Join(err, finish())
		}
	}
	defer func() { returned = errors.Join(returned, finish()) }()
	if err := requireHostStatePayloadObservation(native, h, observation); err != nil {
		return nil, err
	}
	attempt, _ := native.Value(hostTransportAttemptKey{}).(*hostTransportAttempt)
	if err := checkHostTransportAttempt(native, attempt, h); err != nil {
		return nil, err
	}
	if !attempt.matchesRegistrationTransportV1(transport) {
		return nil, baselineErrorV1("the actual registration transport changed before native Start")
	}

	process, err := transport.Start(native, baselineCommandV1)
	if err != nil {
		return nil, err
	}
	if err := process.Stdin().Close(); err != nil {
		process.Kill()
		return nil, errors.Join(err, process.Wait())
	}
	raw, err = io.ReadAll(io.LimitReader(process.Stdout(), 16<<10+1))
	if err != nil || len(raw) > 16<<10 {
		process.Kill()
		return nil, errors.Join(baselineErrorV1("the read-only registration baseline exceeded its fixed bound"), err, process.Wait())
	}
	waitErr := process.Wait()
	if err := checkHostTransportAttempt(native, attempt, h); err != nil {
		return nil, errors.Join(err, waitErr)
	}
	if !attempt.matchesRegistrationTransportV1(transport) {
		return nil, baselineErrorV1("the actual registration transport changed during native Wait")
	}
	if err := requireHostStatePayloadObservation(native, h, observation); err != nil {
		return nil, errors.Join(err, waitErr)
	}
	return raw, waitErr
}
