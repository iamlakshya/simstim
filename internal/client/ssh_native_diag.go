package client

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// nativeSSHDiagnosis holds facts from the complete agent-only transcript.
// It retains no host text and uses the ordinary SSH diagnostic patterns.
type nativeSSHDiagnosis struct {
	stopped    bool
	shownType  string
	shown      string
	failReason string
}

func scanNativeSSHDiagnosis(reader io.Reader, alias string) (nativeSSHDiagnosis, error) {
	var out nativeSSHDiagnosis
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), agentNativeLogLine+1)
	changed, unknown, shown := 0, 0, 0
	changedAlias, unknownAlias := true, true
	authenticated, lastFailed := false, false
	unknownType, shownType, shownFingerprint := "", "", ""
	reason := len(sshFailures)
	var previous, pair []byte
	for scanner.Scan() {
		line := bytes.TrimRight(scanner.Bytes(), "\r")
		if len(bytes.TrimSpace(line)) != 0 {
			lastFailed = failedRE.Match(line)
		}
		authenticated = authenticated || authRE.Match(line) || deniedRE.Match(line)
		for i := range reason {
			if sshFailures[i].re.Match(line) {
				reason = i
				break
			}
		}
		if match := changedRE.FindSubmatch(line); match != nil {
			changed = min(changed+1, 2)
			changedAlias = changedAlias && strings.EqualFold(string(match[1]), alias)
		}
		if match := unknownRE.FindSubmatch(line); match != nil {
			unknown = min(unknown+1, 2)
			unknownAlias = unknownAlias && strings.EqualFold(string(match[2]), alias)
			unknownType = string(match[1])
		}
		if len(previous) != 0 {
			pair = append(pair[:0], previous...)
			pair = append(pair, '\n')
			pair = append(pair, line...)
			if match := shownRE.FindSubmatch(pair); match != nil {
				shown = min(shown+1, 2)
				shownType, shownFingerprint = string(match[1]), string(match[2])
			}
		}
		previous = previous[:0]
		if bytes.HasPrefix(line, []byte("The fingerprint for the ")) {
			previous = append(previous, line...)
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("classify the complete native SSH transcript: %w", err)
	}
	if reason < len(sshFailures) {
		out.failReason = sshFailures[reason].says
	}
	if authenticated || !lastFailed {
		return out, nil
	}
	if changed > 0 {
		if changedAlias {
			out.stopped, out.shownType = true, "?"
			if shown == 1 {
				out.shownType, out.shown = shownType, shownFingerprint
			}
		}
		return out, nil
	}
	if unknown > 0 && unknownAlias {
		out.stopped, out.shownType = true, "?"
		if unknown == 1 {
			out.shownType = unknownType
		}
	}
	return out, nil
}

func (p *sshProc) nativeSSHDiag() (nativeSSHDiagnosis, int, bool) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return p.nativeDiag, p.code, p.nativeClassified
}

// rawExited separates process lifetime from bounded final log classification.
func (p *sshProc) rawExited() <-chan struct{} { return p.rawDone }

func (p *sshProc) agentShutdownFailed() {
	if p.logFile != nil && p.logFile.agent != nil {
		p.logFile.agent.recordFailure(agentNativeLogError("the owned process and final diagnostics exceeded their shutdown deadline"), true)
	}
}
