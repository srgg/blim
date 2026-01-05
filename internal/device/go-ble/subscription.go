package goble

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/groutine"
)

// ----------------------------
// Subscription
// ----------------------------

// drainChannel removes and releases all pending values from a BLEValue channel.
// This is used to discard any cached values that CoreBluetooth delivers when
// notifications are first enabled via SetNotify(true). These are stale cached
// values from discovery, not real-time notifications.
func drainChannel(ch chan *BLEValue) {
	for {
		select {
		case val := <-ch:
			val.release() // Return to pool
		default:
			return // Channel empty
		}
	}
}

func newRecord(mode device.StreamMode) *device.Record {
	r := &device.Record{
		TsUs: time.Now().UnixMicro(),
	}
	if mode == device.StreamBatched {
		r.BatchValues = make(map[string][][]byte)
	} else {
		r.Values = make(map[string][]byte)
	}
	return r
}

type Subscription struct {
	Name     string // Identifier for logging (user-provided or auto-generated)
	Chars    []*BLECharacteristic
	Mode     device.StreamMode
	MaxRate  time.Duration
	Callback func(*device.Record)

	ctx    context.Context
	cancel context.CancelFunc

	// Per-subscription channel for broadcast delivery from fan-out goroutines
	updates chan *BLEValue
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
	// Use provided name or auto-generate
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

// ----------------------------
// Fan-out Methods (on BLECharacteristic)
// ----------------------------

// registerSubscriber adds a channel to the fan-out registry for a subscription.
// Starts the fan-out goroutine on the first subscriber (reference-counted lifecycle).
func (c *BLECharacteristic) registerSubscriber(ch chan *BLEValue) {
	c.subChannelsMu.Lock()
	defer c.subChannelsMu.Unlock()

	c.subChannels = append(c.subChannels, ch)

	// Start fan-out goroutine on first subscriber
	if len(c.subChannels) == 1 {
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

	// Stop fan-out goroutine when last subscriber leaves
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
func (c *BLEConnection) Subscribe(opts []*device.SubscribeOptions, mode device.StreamMode, maxRate time.Duration, callback func(*device.Record)) (func(), error) {
	return c.SubscribeWithName("", opts, mode, maxRate, callback)
}

// SubscribeWithName subscribes to notifications from multiple services and characteristics with streaming patterns.
// The name parameter is used for logging; if empty, an auto-generated name is used.
//
// Supports advanced subscription with streaming patterns and callbacks:
//
//	connection.SubscribeWithName("heartrate", []*device.SubscribeOptions{
//	  { Service: "0000180d-0000-1000-8000-00805f9b34fb", Characteristics: []string{"00002a37-0000-1000-8000-00805f9b34fb"} },
//	}, device.StreamEveryUpdate, 0, func(record *device.Record) { ... })
func (c *BLEConnection) SubscribeWithName(name string, opts []*device.SubscribeOptions, mode device.StreamMode, maxRate time.Duration, callback func(*device.Record)) (func(), error) {
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

	sub := &Subscription{
		Name:     name,
		Chars:    allCharacteristics,
		Mode:     mode,
		MaxRate:  maxRate,
		Callback: callback,
		updates:  make(chan *BLEValue, DefaultChannelBuffer),
	}
	sub.ctx, sub.cancel = context.WithCancel(c.ctx)

	// Register with each characteristic for fan-out broadcast
	for _, char := range allCharacteristics {
		char.registerSubscriber(sub.updates)
	}

	// Add subscription to manager and start goroutine
	c.subMgr.Add(sub, c.runSubscription)

	return sub.cancel, nil
}

func (c *BLEConnection) runSubscription(sub *Subscription) {
	defer c.subMgr.Done()

	// Cleanup: unregister from all characteristics when subscription exits
	defer func() {
		for _, char := range sub.Chars {
			char.unregisterSubscriber(sub.updates)
		}
		// Drain any remaining values in our channel
		drainChannel(sub.updates)
	}()

	// Recover from panics in subscription callback to prevent crash
	defer func() {
		if r := recover(); r != nil {
			if c.logger != nil {
				c.logger.WithFields(map[string]interface{}{
					"panic":          r,
					"connection_ptr": fmt.Sprintf("%p", c),
				}).Error("Subscription callback panicked")
			}
		}
	}()

	if c.logger != nil {
		c.logger.WithField("sub", sub.Name).Debug("Subscription goroutine started")
	}
	defer func() {
		if c.logger != nil {
			c.logger.WithField("sub", sub.Name).Debug("Subscription goroutine exiting")
		}
	}()

	// CoreBluetooth delivers cached characteristic values asynchronously over ~100-200ms
	// after SetNotify(true) returns. Wait for the delivery to complete, then drain once.
	// DrainDuration can be set to 0 in tests to skip this delay.
	if c.DrainDuration > 0 {
		time.Sleep(c.DrainDuration)
		drainChannel(sub.updates)
	}

	// StreamEveryUpdate: process each value immediately as it arrives
	if sub.Mode == device.StreamEveryUpdate {
		for {
			// NOTE: COLD→HOT ordering intentional. With HOT first, high-frequency data
			// would starve ctx.Done() checks, preventing responsive shutdown.
			select {
			case <-sub.ctx.Done():
				return
			case val := <-sub.updates:
				if val.Char == nil {
					val.release()
					continue
				}
				if c.logger != nil {
					c.logger.WithFields(logrus.Fields{
						"char": val.Char.UUID(),
						"len":  len(val.Data),
						"time": time.Now().Format("15:04:05.000"),
					}).Debug("[SUB] dequeue from channel")
				}
				record := newRecord(device.StreamEveryUpdate)
				record.Values[val.Char.UUID()] = val.Data
				record.TsUs = val.TsUs
				if val.Flags != 0 {
					record.Flags |= val.Flags
				}
				// IMPORTANT: record.Values contain references to pooled BLEValue.Data slices.
				// After this callback returns, the BLEValue is released back to the pool (see val.release() and
				// its Data slice will be reused for subsequent notifications. Callbacks that
				// store records for later processing MUST deep-copy record.Values entries.
				sub.Callback(record)
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
	batchValues := make(map[string][][]byte)   // StreamBatched: all values per char
	latestValues := make(map[string]*BLEValue) // StreamAggregated: latest value per char

	for {
		// NOTE: COLD→HOT ordering intentional. With HOT first, high-frequency data
		// would starve ctx.Done() checks, preventing responsive shutdown.
		select {
		case <-sub.ctx.Done():
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
			charUUID := val.Char.UUID()

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
			if sub.Mode == device.StreamBatched {
				if len(batchValues) > 0 {
					record := newRecord(device.StreamBatched)
					record.BatchValues = batchValues
					record.TsUs = time.Now().UnixMicro()
					sub.Callback(record)
					// Reset for next batch
					batchValues = make(map[string][][]byte)
				}
			} else {
				// StreamAggregated
				if len(latestValues) > 0 {
					record := newRecord(device.StreamAggregated)
					for charUUID, val := range latestValues {
						record.Values[charUUID] = val.Data
						if val.Flags != 0 {
							record.Flags |= val.Flags
						}
						if val.TsUs > record.TsUs {
							record.TsUs = val.TsUs
						}
						val.release()
					}
					sub.Callback(record)
					// Reset for next interval
					latestValues = make(map[string]*BLEValue)
				}
			}
		}
	}
}
