package grpckit

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

// startHealthWatch включает периодическую оценку проб группы health: без неё
// gRPC health отвечает SERVING даже при недоступной БД, и оркестратор шлёт
// трафик в неработающий под.
func (s *Server) startHealthWatch() {
	if len(s.probes) == 0 || s.cfg.HealthInterval <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthStop != nil || s.stopped {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.healthStop, s.healthDone = cancel, done
	go s.watchProbes(ctx, done)
}

// stopHealthWatch снимает наблюдение до health.Shutdown, иначе очередной тик
// вернул бы SERVING уже останавливающемуся серверу.
func (s *Server) stopHealthWatch() {
	s.mu.Lock()
	cancel, done := s.healthStop, s.healthDone
	s.healthStop, s.healthDone, s.stopped = nil, nil, true
	s.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (s *Server) watchProbes(ctx context.Context, done chan struct{}) {
	defer close(done)

	ticker := time.NewTicker(s.cfg.HealthInterval)
	defer ticker.Stop()

	serving := true
	for {
		serving = s.evaluateProbes(ctx, serving)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// evaluateProbes возвращает новый overall-статус; логируется только смена
// статуса — иначе каждый тик писал бы строку в лог.
func (s *Server) evaluateProbes(ctx context.Context, was bool) bool {
	name, err := s.runProbes(ctx)
	if ctx.Err() != nil {
		return was
	}

	serving := err == nil
	if serving == was {
		return serving
	}
	if serving {
		s.SetServing("", true)
		s.log.Info(ctx, "grpckit: health serving")
		return true
	}
	s.SetServing("", false)
	s.log.Warn(ctx, "grpckit: health not serving", "probe", name, "error", err.Error())
	return false
}

// runProbes возвращает имя и ошибку первой упавшей пробы: NOT_SERVING ставится
// по первому отказу, дальнейшие проверки уже ничего не меняют. Дедлайн пробы —
// GRPC_HEALTH_PROBE_TIMEOUT, а не период тика: иначе зависшая зависимость
// растягивала бы цикл переоценки на N × GRPC_HEALTH_INTERVAL.
func (s *Server) runProbes(ctx context.Context) (string, error) {
	for _, p := range s.probes {
		if err := healthkit.WithTimeout(p, s.cfg.HealthProbeTimeout).Run(ctx); err != nil {
			return p.Name, err
		}
	}
	return "", nil
}
