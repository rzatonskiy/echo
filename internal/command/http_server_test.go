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
		e           = echo.New()
		ctx, cancel = context.WithCancel(context.Background())
		done        = make(chan error, 1)
	)

	defer cancel()

	go func() {
		done <- runServer(ctx, e, "127.0.0.1:0", zap.NewNop())
	}()

	waitServing(t, e)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("runServer did not return after the context cancellation")
	}
}

// TestRunServerStartError ensures a failed start is reported as is and does not wait for the context.
func TestRunServerStartError(t *testing.T) {
	var busy, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() {
		require.NoError(t, busy.Close())
	}()

	var (
		e    = echo.New()
		done = make(chan error, 1)
	)

	// the context is never cancelled, so only an early return can unblock the test
	go func() {
		done <- runServer(context.Background(), e, busy.Addr().String(), zap.NewNop())
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("runServer hung on a busy address instead of returning the start error")
	}
}

// waitServing blocks until the server answers a request. A non nil ListenerAddr is not enough here:
// echo opens the listener before entering Serve, so shutting down on it would test another path.
func waitServing(t *testing.T, e *echo.Echo) {
	t.Helper()

	var (
		client   = http.Client{Timeout: time.Second}
		deadline = time.Now().Add(15 * time.Second)
	)

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

		return
	}

	t.Fatal("the server did not start serving")
}
