package goble

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/groutine"
)

// ----------------------------
// Subscription
// ----------------------------

// maxDrainIterations is the safety limit to prevent infinite spin
// if the sender is still active
const maxDrainIterations = 1000

// drainChannel removes and releases all pending values from a BLEValue channel.
// This is used to discard any cached values that CoreBluetooth delivers when
// notifications are first enabled via SetNotify(true). These are stale cached
// values from discovery, not real-time notifications.
func drainChannel(ch chan *BLEValue) int {
	count := 0
	for count < maxDrainIterations {
		select {
		case val := <-ch:
			val.release()
			count++
		default:
			return count
		}
	}
	return count
}

func newRecord(mode device.StreamMode) *device.Record {
	r := &device.Record{
		RecordMeta: device.RecordMeta{
			TsUs: time.Now().UnixMicro(),
		},
	}

	switch mode {
	case device.StreamBatched:
		r.BatchValues = make(map[string][][]byte)
	default:
		r.Values = make(map[string][]byte)
	}

	return r
}

type Subscription struct {
	Name          string // Identifier for logging (user-provided or auto-generated)
	Chars         []*BLECharacteristic
	Mode          device.StreamMode
	MaxRate       time.Duration
	DrainDuration time.Duration // Max wait to discard stale cached values before delivering fresh notifications
	Callback      func(*device.Record)

	ctx    context.Context
	cancel context.CancelFunc

	// Per-subscription channel for broadcast delivery from fan-out goroutines
	updates chan *BLEValue

	Diag *SubscriptionDiagnostics // nil in production, populated in test
}

// ----------------------------
// Subscription Manager
// ----------------------------

// SubscriptionManager manages the lifecycle of Lua subscriptions
type SubscriptionManager struct {
	subscriptions []*Subscription
	wg            sync.WaitGroup
	mu            sync.Mutex
	logger        *logrus.Logger
}

// NewSubscriptionManager creates a new subscription manager
func NewSubscriptionManager(logger *logrus.Logger) *SubscriptionManager {
	return &SubscriptionManager{
		subscriptions: make([]*Subscription, 0),
		logger:        logger,
	}
}

// Add adds a subscription to the manager and starts its goroutine
func (m *SubscriptionManager) Add(sub *Subscription, runner func(*Subscription)) {
	m.mu.Lock()
	m.subscriptions = append(m.subscriptions, sub)
	idx := len(m.subscriptions)
	// Use the provided name or auto-generate
	if sub.Name == "" {
		sub.Name = fmt.Sprintf("sub-%d", idx)
	}
	m.mu.Unlock()

	m.wg.Add(1)

	groutine.Go(nil, sub.Name, func(ctx context.Context) {
		runner(sub)
	})
}

// CancelAll cancels all active subscriptions and clears the list
func (m *SubscriptionManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sub := range m.subscriptions {
		if sub.cancel != nil {
			sub.cancel()
		}
	}
	m.subscriptions = nil
}

// Wait waits for all subscription goroutines to complete
func (m *SubscriptionManager) Wait() {
	if m.logger != nil {
		m.logger.Debug("Waiting for subscription goroutines to complete...")
	}
	m.wg.Wait()
	if m.logger != nil {
		m.logger.Debug("All subscription goroutines completed")
	}
}

// Done decrements the wait group counter (called by subscription goroutines)
func (m *SubscriptionManager) Done() {
	m.wg.Done()
}

// GetByName returns the subscription with the given name, or nil if not found.
func (m *SubscriptionManager) GetByName(name string) *Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.subscriptions {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

// ----------------------------
// Fan-out Methods (on BLECharacteristic)
// ----------------------------

// registerSubscriber adds a channel to the fan-out registry for a subscription.
// Starts the fan-out goroutine on the first subscriber (reference-counted lifecycle).
// Returns true if this was the first subscriber (i.e., we started the fan-out).
func (c *BLECharacteristic) registerSubscriber(ch chan *BLEValue) bool {
	c.subChannelsMu.Lock()
	defer c.subChannelsMu.Unlock()

	isFirst := len(c.subChannels) == 0
	c.subChannels = append(c.subChannels, ch)

	// Start fan-out goroutine on first subscriber
	if isFirst {
		// Parent context ensures fan-out exits on connection disconnect
		parentCtx := context.Background()
		if c.connection != nil && c.connection.ctx != nil {
			parentCtx = c.connection.ctx
		}
		c.fanOutCtx, c.fanOutCancel = context.WithCancel(parentCtx)
		groutine.Go(c.fanOutCtx, fmt.Sprintf("fanout-%s", c.uuid), func(ctx context.Context) {
			c.runFanOut(ctx)
		})
	}
	return isFirst
}

// unregisterSubscriber removes a subscription's channel from the fan-out registry.
// Stops the fan-out goroutine when the last subscriber leaves (reference-counted lifecycle).
func (c *BLECharacteristic) unregisterSubscriber(ch chan *BLEValue) {
	c.subChannelsMu.Lock()
	defer c.subChannelsMu.Unlock()

	for i, sub := range c.subChannels {
		if sub == ch {
			c.subChannels = append(c.subChannels[:i], c.subChannels[i+1:]...)
			break
		}
	}

	// Stop fan-out goroutine when the last subscriber leaves
	if len(c.subChannels) == 0 && c.fanOutCancel != nil {
		c.fanOutCancel()
		c.fanOutCancel = nil
		c.fanOutCtx = nil
	}
}

// runFanOut reads from char.updates and distributes values to all registered subscriber channels.
// Uses non-blocking sends with drop policy (same as EnqueueValue).
func (c *BLECharacteristic) runFanOut(ctx context.Context) {
	for {
		// NOTE: COLD→HOT ordering intentional. With HOT first, high-frequency data
		// would starve ctx.Done() checks are performed, preventing a responsive shutdown.
		select {
		case <-ctx.Done():
			// Drain remaining values before exit to avoid pool leaks
			for {
				select {
				case v := <-c.updates:
					v.release()
				default:
					return
				}
			}
		case v, ok := <-c.updates:
			if !ok {
				return // Channel closed (connection teardown)
			}

			c.subChannelsMu.Lock()
			numSubs := len(c.subChannels)

			for i, out := range c.subChannels {
				var valCopy *BLEValue
				if i == numSubs-1 {
					valCopy = v // Last subscriber gets original
				} else {
					valCopy = v.poolCopy() // Others get copies
				}

				select {
				case out <- valCopy:
					// Delivered
				default:
					// Buffer full - release copy back to pool
					if c.connection != nil && c.connection.logger != nil {
						c.connection.logger.WithField("char", c.uuid).Warn("[FanOut] subscriber buffer full, dropped value")
					}
					valCopy.release()
				}
			}

			c.subChannelsMu.Unlock()

			// If no subscribers, release the value
			if numSubs == 0 {
				v.release()
			}
		}
	}
}

// ----------------------------
// Subscription Methods
// ----------------------------

// Subscribe subscribes to notifications with an auto-generated name.
// See SubscribeWithName for full documentation.
func (c *BLEConnection) Subscribe(opts []*device.SubscribeOptions, mode device.StreamMode, maxRate time.Duration, drainDuration time.Duration, callback func(*device.Record)) (func(), error) {
	return c.SubscribeWithName("", opts, mode, maxRate, drainDuration, callback)
}

// SubscribeWithName subscribes to notifications from multiple services and characteristics with streaming patterns.
// The name parameter is used for logging; if empty, an auto-generated name is used.
// drainDuration specifies how long to wait discarding stale cached values before delivering fresh notifications.
//
// Supports advanced subscription with streaming patterns and callbacks:
//
//	connection.SubscribeWithName("heartrate", []*device.SubscribeOptions{
//	  { Service: "0000180d-0000-1000-8000-00805f9b34fb", Characteristics: []string{"00002a37-0000-1000-8000-00805f9b34fb"} },
//	}, device.StreamEveryUpdate, 0, 500*time.Millisecond, func(record *device.Record) { ... })
func (c *BLEConnection) SubscribeWithName(name string, opts []*device.SubscribeOptions, mode device.StreamMode, maxRate time.Duration, drainDuration time.Duration, callback func(*device.Record)) (func(), error) {
	// Error prefix for this subscription
	errPrefix := "subscription"
	if name != "" {
		errPrefix = fmt.Sprintf("subscription %q", name)
	}

	// Validate parameters before acquiring any locks or allocating resources
	if callback == nil {
		return nil, fmt.Errorf("%s: no callback specified", errPrefix)
	}

	if len(opts) == 0 {
		return nil, fmt.Errorf("%s: no services specified", errPrefix)
	}

	c.logger.WithFields(map[string]interface{}{
		"name":     name,
		"services": len(opts),
		"mode":     mode,
	}).Debug("Subscribe called - about to create goroutine")

	c.connMutex.Lock()

	// Check if connected (we already hold the lock, so use a safe version)
	if !c.isConnectedInternal() {
		c.connMutex.Unlock()
		return nil, fmt.Errorf("%s: %w", errPrefix, device.ErrNotConnected)
	}

	// Validate subscription options and get characteristics from all services
	var allCharacteristics []*BLECharacteristic
	for _, opt := range opts {
		characteristicsToSubscribe, err := c.validateSubscribeOptions(opt, true)
		if err != nil {
			c.connMutex.Unlock()
			return nil, fmt.Errorf("%s: validation failed: %w", errPrefix, err)
		}

		// Convert validated BLECharacteristics for Subscription
		for _, bleChar := range characteristicsToSubscribe {
			allCharacteristics = append(allCharacteristics, bleChar)
		}
	}

	// If no characteristics support notifications after validation
	if len(allCharacteristics) == 0 {
		c.connMutex.Unlock()
		return nil, fmt.Errorf("%s: no characteristics available across all specified services", errPrefix)
	}

	// Release lock before calling BLESubscribe (which acquires its own locks)
	c.connMutex.Unlock()

	// CRITICAL: Enable BLE notifications by calling BLESubscribe for each service
	// This must be done outside the lock to avoid deadlock (BLESubscribe acquires its own locks)
	for _, opt := range opts {
		// BLESubscribe will validate again and call the client.Subscribe() to enable CCCD (Client Characteristic Configuration Descriptor)
		err := c.BLESubscribe(opt)
		if err != nil {
			return nil, fmt.Errorf("%s: failed to enable BLE notifications: %w", errPrefix, err)
		}
	}

	// Re-acquire lock for creating a subscription
	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	// Re-check connection after re-acquiring lock (disconnect could have occurred during BLESubscribe)
	if !c.isConnectedInternal() {
		return nil, fmt.Errorf("%s: %w", errPrefix, device.ErrNotConnected)
	}

	// Create a subscription with diagnostics (nil in production)
	sub := &Subscription{
		Name:          name,
		Chars:         allCharacteristics,
		Mode:          mode,
		MaxRate:       maxRate,
		DrainDuration: drainDuration,
		Callback:      callback,
		updates:       make(chan *BLEValue, DefaultChannelBuffer),
		Diag:          newSubscriptionDiagnostics(allCharacteristics),
	}
	sub.ctx, sub.cancel = context.WithCancel(c.ctx)

	// Register with each characteristic for fan-out broadcast
	for _, char := range allCharacteristics {
		isFirst := char.registerSubscriber(sub.updates)
		if isFirst {
			sub.Diag.MarkFirstSubscriber(char.UUID()) // safe even if Diag nil
		}
	}

	// Set references for cancel verification
	sub.Diag.SetReferences(sub, c)

	// Track subscription at connection level
	c.ConnDiag.TrackSubscription(sub)

	// Add subscription to manager and start goroutine
	c.subMgr.Add(sub, c.runSubscription)

	// Return wrapped cancel (no-op wrapper in prod, verification in test)
	return wrapCancelFunc(sub.Diag, sub.cancel), nil
}

func (c *BLEConnection) runSubscription(sub *Subscription) {
	defer c.subMgr.Done()

	// Cleanup: unregister from all characteristics when subscription exits
	defer func() {
		for _, char := range sub.Chars {
			char.unregisterSubscriber(sub.updates)
		}

		// Drain any remaining values (with count for diagnostics)
		drained := drainChannel(sub.updates)
		sub.Diag.AddDrained(drained)
		sub.Diag.MarkCleanedUp()
	}()

	// Recover from panics in subscription callback to prevent crash
	defer func() {
		if r := recover(); r != nil {
			sub.Diag.MarkPanicRecovered()
			if c.logger != nil {
				c.logger.WithFields(map[string]interface{}{
					"panic":          r,
					"connection_ptr": fmt.Sprintf("%p", c),
				}).Error("Subscription callback panicked: sub=", sub.Name)
			}
		}
	}()

	if c.logger != nil {
		c.logger.Debug("Subscription goroutine started: sub=", sub.Name)
	}
	defer func() {
		if c.logger != nil {
			c.logger.Debug("Subscription goroutine exiting: sub=", sub.Name)
		}
	}()

	// Drain stale cached values that may arrive asynchronously after subscription starts.
	// The drain exits early when no more values arrive (stale flow stopped), or after
	// DrainDuration elapses—whichever comes first. Set DrainDuration to 0 to disable.
	if sub.DrainDuration > 0 {
		const drainQuietPeriod = 50 * time.Millisecond
		deadline := time.Now().Add(sub.DrainDuration)
		totalDrained := 0

		for time.Now().Before(deadline) {
			// Drain any immediately available values
			drained := drainChannel(sub.updates)
			totalDrained += drained

			// Wait for more values or quiet period timeout
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			waitTime := min(drainQuietPeriod, remaining)

			select {
			case val := <-sub.updates:
				// More values arriving, continue draining
				val.release()
				totalDrained++
			case <-time.After(waitTime):
				// No values arrived within quiet period—stale flow stopped, exit early
				sub.Diag.AddDrained(totalDrained)
				goto drainComplete
			case <-sub.ctx.Done():
				sub.Diag.AddDrained(totalDrained)
				return
			}
		}
		sub.Diag.AddDrained(totalDrained)
	}
drainComplete:

	// StreamEveryUpdate: process each value immediately as it arrives
	if sub.Mode == device.StreamEveryUpdate {
		for {
			// NOTE: COLD→HOT ordering intentional. With HOT first, high-frequency data
			// would starve ctx.Done() checks, preventing responsive shutdown.
			select {
			case <-sub.ctx.Done():
				sub.Diag.MarkImplicitCancel()
				return
			case val := <-sub.updates:
				if val.Char == nil {
					val.release()
					continue
				}
				sub.Diag.MarkFirstValue() // only sets once
				if c.logger != nil {
					c.logger.WithFields(logrus.Fields{
						"char": val.Char.UUID(),
						"len":  len(val.Data),
						"time": time.Now().Format("15:04:05.000"),
					}).Debugf("[SUB] dequeue from channel: sub=%s, char=%s, len=%d", sub.Name, val.Char.UUID(), len(val.Data))
				}
				record := newRecord(device.StreamEveryUpdate)
				record.Values[val.Char.UUID()] = val.Data
				record.Seq = val.Seq
				record.TsUs = val.TsUs
				record.Flags |= val.Flags

				// IMPORTANT: record.Values contain references to pooled BLEValue.Data slices.
				// After this callback returns, the BLEValue is released back to the pool (see val.release() and
				// its Data slice will be reused for subsequent notifications. Callbacks that
				// store records for later processing MUST deep-copy record.Values entries.
				sub.Callback(record)
				sub.Diag.IncrementCallback()
				sub.Diag.IncrementProcessed()
				if c.logger != nil {
					c.logger.Debug("[subscription] callback returned")
				}
				val.release()
			}
		}
	}

	// StreamBatched or StreamAggregated: collect values between ticks
	if sub.MaxRate <= 0 {
		sub.MaxRate = DefaultBatchedInterval
	}
	ticker := time.NewTicker(sub.MaxRate)
	defer ticker.Stop()

	// Buffers for batched/aggregated modes
	batchValues := make(map[string][][]byte) // StreamBatched: all values per char
	batchMeta := make(map[string][]*device.RecordMeta)
	latestValues := make(map[string]*BLEValue) // StreamAggregated: latest value per char

	for {
		// NOTE: COLD→HOT ordering intentional. With HOT first, high-frequency data
		// would starve ctx.Done() checks, preventing responsive shutdown.
		select {
		case <-sub.ctx.Done():
			sub.Diag.MarkImplicitCancel()
			// Release any held values in aggregated mode
			for _, v := range latestValues {
				v.release()
			}
			return

		case val := <-sub.updates:
			if val.Char == nil {
				val.release()
				continue
			}
			sub.Diag.MarkFirstValue() // only sets once
			charUUID := val.Char.UUID()

			// Subscription mode either: StreamBatched or StreamAggregated
			batchMeta[charUUID] = append(batchMeta[charUUID], &device.RecordMeta{
				Seq:   val.Seq,
				TsUs:  val.TsUs,
				Flags: val.Flags,
			})
			sub.Diag.IncrementProcessed()

			if sub.Mode == device.StreamBatched {
				// Accumulate all values for each characteristic
				// Copy data since we release the value
				dataCopy := make([]byte, len(val.Data))
				copy(dataCopy, val.Data)
				batchValues[charUUID] = append(batchValues[charUUID], dataCopy)
				val.release()
			} else {
				// StreamAggregated: keep only the latest value per char
				if old := latestValues[charUUID]; old != nil {
					old.release()
				}
				latestValues[charUUID] = val
			}

		case <-ticker.C:
			// Subscription mode either: StreamBatched or StreamAggregated
			record := newRecord(sub.Mode)
			record.Meta = batchMeta

			// Set max(TsUs, Seq) across all batches
			for _, batches := range batchMeta {
				for _, rm := range batches {
					record.TsUs = max(rm.TsUs, record.TsUs)
					record.Seq = max(rm.Seq, record.Seq)
				}
			}
			record.TsUs = time.Now().UnixMicro()

			if sub.Mode == device.StreamBatched {
				if len(batchValues) > 0 {
					record.BatchValues = batchValues

					sub.Callback(record)
					sub.Diag.IncrementCallback()

					// Reset for next batch
					// TODO: SLAB ALLOCATOR FOR SLICES
					batchValues = make(map[string][][]byte)
				}
			} else {
				// StreamAggregated
				if len(latestValues) > 0 {
					for charUUID, val := range latestValues {
						record.Values[charUUID] = val.Data
						if val.Flags != 0 {
							record.Flags |= val.Flags
						}
						if val.TsUs > record.TsUs {
							record.TsUs = val.TsUs
						}
					}
					sub.Callback(record)
					sub.Diag.IncrementCallback()
					// Release pooled values AFTER callback completes to avoid data race
					for _, val := range latestValues {
						val.release()
					}
					// Reset for next interval
					latestValues = make(map[string]*BLEValue)
				}
			}

			// Reset Meta for next interval
			batchMeta = make(map[string][]*device.RecordMeta)
		}
	}
}
