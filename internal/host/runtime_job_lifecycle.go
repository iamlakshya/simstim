package host

import "errors"

// done joins the actual fetch handler. Cleanup success is separately proven
// by the same retained resource owner, map removal and slot release below.
func (s *runtimeService) finishRuntimeJobV1(token string, job *runtimeJob) error {
	job.finished.Do(func() { job.cancel(); close(job.done) })
	return s.releaseRuntimeJobV1(token, job)
}

func (s *runtimeService) releaseRuntimeJobV1(token string, job *runtimeJob) error {
	job.cleanupMu.Lock()
	defer job.cleanupMu.Unlock()
	if job.resource != nil {
		if err := job.resource.close(); err != nil {
			return err
		}
	}
	job.released.Do(func() {
		s.mu.Lock()
		if s.jobs[token] == job {
			delete(s.jobs, token)
			delete(s.downloads, token)
		}
		s.mu.Unlock()
		<-runtimeSlots
	})
	return nil
}

func (s *runtimeService) cancelRuntimeJobV1(token string) bool {
	found, _ := s.cancelRuntimeJobCleanupV1(token)
	return found
}
func (s *runtimeService) cancelRuntimeJobCleanupV1(token string) (bool, error) {
	s.mu.Lock()
	job := s.jobs[token]
	if job == nil {
		s.mu.Unlock()
		return false, nil
	}
	job.cancel()
	queued := !job.started
	if queued {
		job.started = true
	}
	s.mu.Unlock()
	if queued {
		return true, s.finishRuntimeJobV1(token, job)
	}
	<-job.done
	return true, s.releaseRuntimeJobV1(token, job)
}

func (s *runtimeService) closeRuntimeServiceV1() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	s.closed = true
	if s.closeDone == nil {
		s.closeDone = make(chan struct{})
	}
	type retainedJob struct {
		token  string
		job    *runtimeJob
		queued bool
	}
	jobs := make([]retainedJob, 0, len(s.jobs))
	for token, job := range s.jobs {
		job.cancel()
		queued := !job.started
		job.started = true
		jobs = append(jobs, retainedJob{token, job, queued})
	}
	s.mu.Unlock()
	var result error
	for _, retained := range jobs {
		if retained.queued {
			result = errors.Join(result, s.finishRuntimeJobV1(retained.token, retained.job))
		}
	}
	for _, retained := range jobs {
		<-retained.job.done
		result = errors.Join(result, s.releaseRuntimeJobV1(retained.token, retained.job))
	}
	s.active.Wait()
	s.mu.Lock()
	owners := make([]*runtimeOwnedPart, 0, len(s.owned))
	for owner := range s.owned {
		owners = append(owners, owner)
	}
	selections := make(map[string]*selectedRuntimeReceiptV1, len(s.selections))
	for key, receipt := range s.selections {
		selections[key] = receipt
	}
	s.mu.Unlock()
	for _, owner := range owners {
		result = errors.Join(result, s.finishRuntimeOwnerV1(owner))
	}
	result = errors.Join(result, s.retryRuntimeSelectionV1())
	for key, receipt := range selections {
		if err := receipt.Close(); err != nil {
			result = errors.Join(result, err)
		} else {
			s.mu.Lock()
			if s.selections[key] == receipt {
				delete(s.selections, key)
			}
			s.mu.Unlock()
		}
	}
	if result == nil {
		select {
		case <-s.closeDone:
		default:
			close(s.closeDone)
		}
	}
	return result
}
