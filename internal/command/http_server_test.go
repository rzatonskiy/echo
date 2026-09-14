// Copyright 2018 Sergey Novichkov. All rights reserved.
// For the full copyright and license information, please view the LICENSE
// file that was distributed with this source code.

package command

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestRunServerGracefulShutdown ensures a context cancellation stops the server without a data race
// and without reporting the shutdown itself as an error. Run it with -race.
func TestRunServerGracefulShutdown(t *testing.T) {
	var (
		e           = newTestEcho()
		ctx, cancel = context.WithCancel(context.Background())
		done        = make(chan error, 1)
	)

	t.Cleanup(cancel)

	go func() {
		done <- runServer(ctx, e, "127.0.0.1:0", zap.NewNop())
	}()

	var addr = requireServing(t, e)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("runServer did not return after the context cancellation")
	}

	// the address is free again, so the server is really down and not just reported as such
	var listener, err = net.Listen("tcp", addr)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

// TestRunServerStartError ensures a failed start is reported as is and does not wait for the context.
func TestRunServerStartError(t *testing.T) {
	var busy, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = busy.Close()
	})

	var done = make(chan error, 1)

	// the context is never cancelled, so only an early return can unblock the test
	go func() {
		done <- runServer(context.Background(), newTestEcho(), busy.Addr().String(), zap.NewNop())
	}()

	select {
	case err := <-done:
		var opErr *net.OpError
		require.ErrorAs(t, err, &opErr)
		require.Equal(t, "listen", opErr.Op)
		require.NotErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(15 * time.Second):
		t.Fatal("runServer hung on a busy address instead of returning the start error")
	}
}

// newTestEcho builds a server that does not write to the test output.
func newTestEcho() *echo.Echo {
	var e = echo.New()
	e.HideBanner = true
	e.HidePort = true

	return e
}

// requireServing blocks until the server answers a request and returns the address it listens on.
// A non nil ListenerAddr is not enough here: echo opens the listener before entering Serve, so
// shutting down on it would test another path.
func requireServing(t *testing.T, e *echo.Echo) string {
	t.Helper()

	// a dedicated transport, so a kept alive connection neither delays the shutdown below
	// nor survives into another test run
	var client = http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	var deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var addr = e.ListenerAddr()
		if addr == nil {
			time.Sleep(time.Millisecond)
			continue
		}

		var res, err = client.Get("http://" + addr.String() + "/")
		if err != nil {
			time.Sleep(time.Millisecond)
			continue
		}

		require.NoError(t, res.Body.Close())

		return addr.String()
	}

	t.Fatal("the server did not start serving")

	return ""
}
