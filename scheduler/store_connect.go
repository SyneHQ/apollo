package scheduler

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// Only connection pings are retried. Schema operations run once after success.
func pingStore(ctx context.Context, ping func(context.Context) error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := ping(ctx)
		if err == nil || attempt == 4 || !transientStoreConnection(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func transientStoreConnection(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03" || pgErr.Code == "53300"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTemporary || dns.IsTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
