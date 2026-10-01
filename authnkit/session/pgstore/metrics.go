package pgstore

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

// Значения label result у authn_session_issued_total и authn_session_rotations_total,
// reason — у authn_session_revoked_total.
const (
	resultOK      = "ok"
	resultInvalid = "invalid"
	resultExpired = "expired"
	resultReused  = "reused"
	resultError   = "error"

	reasonLogout = "logout"
	reasonAll    = "all"
	reasonReused = "reused"
)

type instruments struct {
	issued         metric.Int64Counter
	rotations      metric.Int64Counter
	revoked        metric.Int64Counter
	cleanupDeleted metric.Int64Counter
}

// newInstruments зовётся в NewStore: инструмент берёт текущий глобальный
// провайдер, а не первый провайдер процесса.
func newInstruments() *instruments {
	m := otelkit.Meter("authnkit/session")
	return &instruments{
		issued: otelkit.Must(m.Int64Counter("authn_session_issued_total",
			metric.WithDescription("Sessions opened by Issue, by result (ok|invalid|error)"))),
		rotations: otelkit.Must(m.Int64Counter("authn_session_rotations_total",
			metric.WithDescription("Refresh token rotations by result (ok|invalid|expired|reused|error)"))),
		revoked: otelkit.Must(m.Int64Counter("authn_session_revoked_total",
			metric.WithDescription("Sessions closed, by reason (logout|all|reused)"))),
		cleanupDeleted: otelkit.Must(m.Int64Counter("authn_session_cleanup_deleted_total",
			metric.WithDescription("Expired sessions deleted by Cleanup"))),
	}
}

func (i *instruments) issue(ctx context.Context, result string) {
	i.issued.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (i *instruments) rotate(ctx context.Context, result string) {
	i.rotations.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (i *instruments) revoke(ctx context.Context, reason string, sessions int64) {
	if sessions > 0 {
		i.revoked.Add(ctx, sessions, metric.WithAttributes(attribute.String("reason", reason)))
	}
}
