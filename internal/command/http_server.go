// Copyright 2018 Sergey Novichkov. All rights reserved.
// For the full copyright and license information, please view the LICENSE
// file that was distributed with this source code.

package command

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gozix/di"
	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/gozix/echo/v4/internal/modifier"
)

const (
	tagEcho       = "echo.echo"
	tagServerName = "echo.server_name"
)

// server is a resolved http server, ready to be run.
type server struct {
	echo *echo.Echo
	addr string
	log  *zap.Logger
}

// NewHTTPServer is command constructor.
func NewHTTPServer(ctn di.Container) *cobra.Command {
	return &cobra.Command{
		Use:   "http-server [name...]",
		Short: "Run http server",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			var modServers *modifier.Modifier
			if modServers, err = modifier.NewModifier(tagServerName, args); err != nil {
				return fmt.Errorf("unable create echo glob modifier : %w", err)
			}

			return ctn.Call(func(serverNames []string, cfg *viper.Viper, logger *zap.Logger) error {
				// resolve every server before starting any of them: the container caches shared
				// definitions with a check-then-set, so resolving one definition from two
				// goroutines closes its ready channel twice, and a failure here must not leave
				// an already started server behind
				var servers = make([]server, 0, len(serverNames))
				for _, srvName := range serverNames {
					var e *echo.Echo
					if err := ctn.Resolve(&e, di.WithTags(tagEcho+"."+srvName)); err != nil {
						return err
					}

					var (
						subCfg = cfg.Sub("echo." + srvName)
						addr   = net.JoinHostPort(
							subCfg.GetString("host"),
							subCfg.GetString("port"),
						)
					)

					servers = append(servers, server{
						echo: e,
						addr: addr,
						log: logger.With(
							zap.String("name", srvName),
							zap.String("addr", addr),
						),
					})
				}

				var wg, ctx = errgroup.WithContext(cmd.Context())
				for _, srv := range servers {
					wg.Go(func() error {
						return runServer(ctx, srv.echo, srv.addr, srv.log)
					})
				}

				return wg.Wait()
			}, di.Constraint(0, modServers.Modifier()))
		},
	}
}

// runServer starts the server and gracefully shuts it down on context cancellation.
//
// The start result travels through a channel instead of a shared variable, so the goroutine running
// the server and the one shutting it down never write to the same memory.
func runServer(ctx context.Context, e *echo.Echo, addr string, log *zap.Logger) error {
	log.Info("Starting HTTP server")

	var errCh = make(chan error, 1)
	go func() {
		var err = e.Start(addr)
		if err != nil {
			log.Info("Gracefully shutting down the HTTP server")
		}

		errCh <- err
	}()

	log.Info("HTTP server started")

	// wait, either an early start failure or a context cancellation
	select {
	case err := <-errCh:
		// a closed server is an expected outcome, not a failure
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err
	case <-ctx.Done():
	}

	// graceful shutdown
	var timeout = 10 * time.Second
	log.Info("Stopping HTTP server", zap.Duration("timeout", timeout))

	var timeoutContext, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// a failed shutdown is reported as is, without joining the serving goroutine below: the
	// listener may still be open, and echo.Shutdown gives up on the plain server whenever the
	// TLS one fails, so the join would block until the process is killed
	if err := e.Shutdown(timeoutContext); err != nil {
		return err
	}

	// join the serving goroutine, so it neither logs nor leaks after the command returns.
	// Shutdown closed the listener, so Start has already returned by now.
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
