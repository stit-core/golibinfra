package grpcstatus

import (
	"testing"

	"google.golang.org/grpc/codes"
)

func TestIsServerFault(t *testing.T) {
	faults := []codes.Code{codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.Unimplemented}
	for _, c := range faults {
		if !IsServerFault(c) {
			t.Errorf("%v must be a server fault", c)
		}
	}
	expected := []codes.Code{
		codes.OK, codes.Canceled, codes.InvalidArgument, codes.DeadlineExceeded, codes.NotFound,
		codes.AlreadyExists, codes.PermissionDenied, codes.ResourceExhausted, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.Unauthenticated,
	}
	for _, c := range expected {
		if IsServerFault(c) {
			t.Errorf("%v must not be a server fault", c)
		}
	}
}
