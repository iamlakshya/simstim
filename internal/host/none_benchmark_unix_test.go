//go:build linux

package host_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func noneBenchmarkLegacyV1(b *testing.B, home string) string {
	b.Helper()
	id := host.NewID()
	now := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "legacy-benchmark", CreatedAt: now, Cwd: home, Agent: host.AgentInfo{Kind: "custom", Argv: []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec cat"}}, Env: host.AgentEnv(os.Environ(), map[string]string{"TERM": "xterm-256color"}), Term: host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24}, Run: host.Runtime{BootID: host.BootID(), Socket: paths.Socket(id)}, Status: host.Status{State: host.StateStarting, Since: now}}
	if err := m.Save(); err != nil {
		b.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	command := exec.Command(executable, "holder", id)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	return id
}

func noneBenchmarkAttachV1(b *testing.B, id string) (*bufio.Reader, *proto.Writer) {
	b.Helper()
	connection, err := net.DialTimeout("unix", paths.Socket(id), 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { connection.Close() })
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	w := proto.NewWriter(connection)
	reader := bufio.NewReader(connection)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "client"}); err != nil {
		b.Fatal(err)
	}
	if frame, err := proto.Read(reader); err != nil || frame.Type != proto.THello {
		b.Fatalf("hello: %v %v", frame.Type, err)
	}
	if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24, LastOffset: 0}); err != nil {
		b.Fatal(err)
	}
	var output []byte
	for !bytes.Contains(output, []byte("READY")) {
		frame, err := proto.Read(reader)
		if err != nil {
			b.Fatal(err)
		}
		if frame.Type == proto.TOutput {
			_, data, err := proto.DecodeOutput(frame.Payload)
			if err != nil {
				b.Fatal(err)
			}
			output = append(output, data...)
		}
	}
	_ = connection.SetDeadline(time.Time{})
	return reader, w
}

// Run explicitly after the runtime fixtures pass by setting
// BENCH_NONE_KEY_TO_PTY=1. Each timed sample sends 1,000 keys through the
// actual holder path and waits for each PTY echo. The two results are evidence
// to collect, never a source-level latency claim.
func BenchmarkNoneKeyToPTY1000(b *testing.B) {
	if os.Getenv("BENCH_NONE_KEY_TO_PTY") != "1" {
		b.Skip("set BENCH_NONE_KEY_TO_PTY=1 to run this live holder benchmark")
	}
	for _, kind := range []string{"legacy", "supervisor"} {
		b.Run(kind, func(b *testing.B) {
			h := hosttest.New(b)
			var id string
			if kind == "legacy" {
				id = noneBenchmarkLegacyV1(b, h.Home)
			} else {
				created, err := host.NewContext(context.Background(), host.NewArgs{Name: "none-benchmark", Wall: "none", Cwd: h.Home, Argv: []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec cat"}, Cols: 80, Rows: 24})
				if err != nil {
					b.Fatal(err)
				}
				id = created.ID
			}
			hosttest.WaitFor(b, 10*time.Second, "benchmark agent readiness", func() bool {
				raw, err := host.HolderRequest(id, "peek", map[string]int{"bytes": 4096}, time.Second)
				var result struct {
					Data []byte `json:"data"`
				}
				return err == nil && json.Unmarshal(raw, &result) == nil && bytes.Contains(result.Data, []byte("READY"))
			})
			reader, writer := noneBenchmarkAttachV1(b, id)
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				for key := 0; key < 1000; key++ {
					if err := writer.Write(proto.TInput, []byte("x")); err != nil {
						b.Fatal(err)
					}
					for {
						frame, err := proto.Read(reader)
						if err != nil {
							b.Fatal(err)
						}
						if frame.Type == proto.TOutput {
							_, data, err := proto.DecodeOutput(frame.Payload)
							if err != nil {
								b.Fatal(err)
							}
							if bytes.Contains(data, []byte("x")) {
								break
							}
						}
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(max(b.N, 1)*1000), "ns/key")
		})
	}
}
