package scheduler

import (
	"context"
	"crypto/tls"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestStoreStartupRetryDoesNotRetryPermanentFailures(t *testing.T) {
	for _, err := range []error{&pq.Error{Code: "28P01"}, &pq.Error{Code: "42501"}, &tls.CertificateVerificationError{Err: errors.New("invalid certificate")}, errors.New("unknown")} {
		calls := 0
		got := pingStore(context.Background(), func(context.Context) error { calls++; return err })
		if got != err || calls != 1 {
			t.Fatal("permanent failure was retried")
		}
	}
	calls := 0
	err := pingStore(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return syscall.ECONNREFUSED
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatal("transient connection did not recover")
	}
}
func TestStoreStartupRetryHonorsTotalDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	calls := 0
	err := pingStore(ctx, func(context.Context) error { calls++; return syscall.ECONNREFUSED })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("startup deadline did not cancel retry")
	}
}
