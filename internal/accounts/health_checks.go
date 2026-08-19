package accounts

type accountHealthCheck struct {
	accountID int64
	publicID  string
}

func (s *Service) queueHealthCheck(record batchAccountRecord) {
	s.healthChecksMu.Lock()
	if _, exists := s.healthChecks[record.id]; exists {
		s.healthChecksMu.Unlock()
		return
	}
	s.healthChecks[record.id] = struct{}{}
	s.healthChecksMu.Unlock()

	select {
	case s.healthCheckQueue <- accountHealthCheck{accountID: record.id, publicID: record.publicID}:
	case <-s.closeCtx.Done():
		s.healthChecksMu.Lock()
		delete(s.healthChecks, record.id)
		s.healthChecksMu.Unlock()
	}
}

func (s *Service) healthCheckWorker() {
	defer s.healthChecksWG.Done()
	for {
		select {
		case job := <-s.healthCheckQueue:
			_ = s.CheckAccount(s.closeCtx, job.publicID)
			s.healthChecksMu.Lock()
			delete(s.healthChecks, job.accountID)
			s.healthChecksMu.Unlock()
		case <-s.closeCtx.Done():
			return
		}
	}
}
