package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// One op is128 complete concurrent native TLS responses on one original
// connection. The random case isolates scheduler overhead with the same
// ownership wrapper. The default case measures the public server's original
// Go1.26 round robin for successful responses; it is not a cancellation API.
func BenchmarkPhysicalH2SchedulerNative128(b *testing.B) {
	for _, variant := range []struct {
		name    string
		factory func() http2.WriteScheduler
	}{
		{"original_default", nil}, {"previous_random", http2.NewRandomWriteScheduler}, {"round_robin", func() http2.WriteScheduler { return newPhysicalH2RoundRobin() }},
	} {
		b.Run(variant.name, func(b *testing.B) { benchmarkPhysicalH2Native128(b, variant.factory) })
	}
}

func benchmarkPhysicalH2Native128(b *testing.B, factory func() http2.WriteScheduler) {
	b.StopTimer()
	answer := `{"padding":"` + strings.Repeat("x", 48<<10) + `","usage":{"total_tokens":7}}`
	answerBytes := []byte(answer)
	meter := NewModelUsageMeterV1()
	_, client, closePair := physicalH2SchedulerPair(b, factory, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := physicalH2RequestDelivery(r)
		turn, refusal, err := meter.BeginV1(ModelUsageRequestV1{Wire: "openai-responses", Path: "/v1/responses", Model: "benchmark"}, 0)
		if err != nil || refusal != nil || stream.retain(turn, nil) != nil {
			b.Error("original benchmark turn admission failed")
			return
		}
		_, err = w.Write(answerBytes)
		stream.completionEvidence(err == nil)
	}))
	b.Cleanup(closePair)
	_ = client.SetDeadline(time.Time{})
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	transport := &http.Transport{Protocols: protocols, ForceAttemptHTTP2: true, DialTLSContext: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	connection, err := transport.NewClientConn(b.Context(), "https", "scheduler.example.test:443")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = connection.Close() })
	jobs := make(chan struct{}, maxActiveFlows)
	results := make(chan error, maxActiveFlows)
	var workers sync.WaitGroup
	var latencyMu sync.Mutex
	var samples [4096]int64
	var count, total uint64
	var longest int64
	for range maxActiveFlows {
		workers.Go(func() {
			for range jobs {
				started := time.Now()
				request, err := http.NewRequestWithContext(b.Context(), http.MethodGet, "https://scheduler.example.test/v1/responses", nil)
				if err == nil {
					var response *http.Response
					response, err = connection.RoundTrip(request)
					if err == nil {
						var read int64
						read, err = io.Copy(io.Discard, response.Body)
						closeErr := response.Body.Close()
						if err == nil {
							err = closeErr
						}
						if err == nil && (response.StatusCode != http.StatusOK || read != int64(len(answer))) {
							err = errPhysicalH2Binding
						}
					}
				}
				elapsed := time.Since(started).Nanoseconds()
				latencyMu.Lock()
				samples[count%uint64(len(samples))] = elapsed
				count++
				total += uint64(elapsed)
				longest = max(longest, elapsed)
				latencyMu.Unlock()
				results <- err
			}
		})
	}
	b.Cleanup(func() { close(jobs); workers.Wait() })
	b.ReportAllocs()
	b.SetBytes(int64(maxActiveFlows * len(answer)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		for range maxActiveFlows {
			jobs <- struct{}{}
		}
		for range maxActiveFlows {
			if err := <-results; err != nil {
				b.Fatal("original native benchmark request failed", err)
			}
		}
	}
	b.StopTimer()
	if count != uint64(b.N)*maxActiveFlows {
		b.Fatal("native benchmark lost or duplicated a response")
	}
	values := append([]int64(nil), samples[:min(count, uint64(len(samples)))]...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) != 0 {
		b.ReportMetric(float64(total)/float64(count), "mean_ns/request")
		b.ReportMetric(float64(longest), "max_ns/request")
		b.ReportMetric(float64(values[(len(values)-1)*99/100]), "p99_tail_ns/request")
	}
}
