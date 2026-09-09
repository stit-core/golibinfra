// Package grpcstatus — общая для grpckit и grpcclient классификация gRPC-кодов:
// какой исход считается сбоем сервера, а какой — ожидаемым.
package grpcstatus

import "google.golang.org/grpc/codes"

// IsServerFault сообщает, стоит ли за кодом сбой сервера (или транспорта до
// него). Остальные не-OK исходы — клиентская ошибка, отказ в доступе, отмена —
// ожидаемые: access-log пишет их на Warn, чтобы алерты на Error не шумели.
func IsServerFault(code codes.Code) bool {
	switch code {
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.Unimplemented:
		return true
	default:
		return false
	}
}
