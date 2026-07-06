package lua

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/groutine"
)

// OutputDrainer continuously drains a Lua output channel to stdout/stderr writers.
// It runs in a background goroutine and provides graceful shutdown via Cancel() and Wait().
type OutputDrainer struct {
	cancelOnce sync.Once      // ensures Cancel() is called at most once
	stop       chan struct{}  // signals the drainer goroutine to stop
	wg         sync.WaitGroup // tracks the drainer goroutine lifecycle
}

// Cancel signals the drainer to stop and drain the remaining output.
func (d *OutputDrainer) Cancel() {
	d.cancelOnce.Do(func() {
		close(d.stop)
	})
}

// Wait blocks until the drainer goroutine has fully exited.
func (d *OutputDrainer) Wait() {
	d.wg.Wait()
}

// drainWithTimeout drains remaining messages from the channel with a timeout.
// Returns true if the channel was closed normally, false if the timeout was reached.
func drainWithTimeout(
	outputChan <-chan LuaOutputRecord,
	stdout, stderr io.Writer,
	timeout time.Duration,
	logger *logrus.Logger,
	reason string,
) bool {
	drainTimeout := time.After(timeout)
	drained := 0
	for {
		select {
		case record, ok := <-outputChan:
			if !ok {
				// Channel closed, all messages drained
				logger.WithFields(logrus.Fields{
					"reason":  reason,
					"drained": drained,
				}).Debug("Output drainer: drain completed (channel closed)")
				return true
			}
			drained++
			var err error
			switch record.Source {
			case "stdout":
				_, err = fmt.Fprint(stdout, record.Content)
			case "stderr":
				_, err = fmt.Fprint(stderr, record.Content)
			}
			if err != nil {
				logger.WithFields(logrus.Fields{
					"source": record.Source,
					"error":  err,
				}).Warn("Output drainer: write failed")
			}
		case <-drainTimeout:
			// Timeout reached, stop draining to prevent goroutine leak
			logger.WithFields(logrus.Fields{
				"reason":  reason,
				"drained": drained,
				"timeout": timeout,
			}).Debug("Output drainer: drain timeout reached")
			return false
		}
	}
}

// NewOutputDrainer starts a goroutine that continuously drains the outputChan
// to the provided stdout/stderr writers. It returns an OutputDrainer
// that you can Cancel() and Wait() on.
func NewOutputDrainer(
	ctx context.Context,
	outputChan <-chan LuaOutputRecord,
	logger *logrus.Logger,
	stdout, stderr io.Writer,
) *OutputDrainer {
	// Use io.Discard for nil writers to eliminate nil checks in the hot path
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	drainer := &OutputDrainer{
		stop: make(chan struct{}),
	}

	drainer.wg.Add(1)
	groutine.Go(ctx, "lua-output-drainer", func(ctx context.Context) {
		defer drainer.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.WithField("panic", r).Error("Output drainer: panic recovered")
			}
		}()
		defer logger.Debugf("%s: exited", groutine.GetName(ctx))

		for {
			select {
			case record, ok := <-outputChan:
				if !ok {
					// Output channel closed by luaAPI
					return
				}
				var err error
				switch record.Source {
				case "stdout":
					_, err = fmt.Fprint(stdout, record.Content)
				case "stderr":
					_, err = fmt.Fprint(stderr, record.Content)
				}
				if err != nil {
					logger.WithFields(logrus.Fields{
						"source": record.Source,
						"error":  err,
					}).Warn("Output drainer: write failed")
				}

			case <-drainer.stop:
				// Drain remaining messages with a timeout to prevent indefinite blocking
				drainWithTimeout(outputChan, stdout, stderr, 100*time.Millisecond, logger, "stop")
				return

			case <-ctx.Done():
				// Context canceled - drain remaining messages with timeout before exit
				drainWithTimeout(outputChan, stdout, stderr, 100*time.Millisecond, logger, "context-done")
				return
			}
		}
	})

	return drainer
}
