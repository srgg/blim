//go:build test

package ptyio

//
//import (
//	"testing"
//
//	"github.com/sirupsen/logrus"
//	"github.com/stretchr/testify/suite"
//)
//
//type PTYRelaySuite struct {
//	suite.Suite
//	logger *logrus.Logger
//}
//
//func (s *PTYRelaySuite) SetupTest() {
//	s.logger = logrus.New()
//	s.logger.SetLevel(logrus.DebugLevel)
//}
//
//func (s *PTYRelaySuite) TestNewPTYRelay() {
//	// GOAL: Verify PTYRelay creation succeeds and initializes in unclosed state
//	//
//	// TEST SCENARIO: Create relay → succeeds → IsClosed returns false
//
//	relay, err := NewPTYRelay(s.logger)
//	s.Require().NoError(err, "NewPTYRelay MUST succeed")
//	s.Require().NotNil(relay, "PTYRelay MUST not be nil")
//	defer relay.Cleanup()
//
//	s.False(relay.IsClosed(), "IsClosed MUST return false initially")
//}
//
//func (s *PTYRelaySuite) TestCloseIdempotent() {
//	// GOAL: Verify Close() can be called multiple times without error (sync.Once behavior)
//	//
//	// TEST SCENARIO: Close three times → all calls succeed → no panic or error
//
//	relay, err := NewPTYRelay(s.logger)
//	s.Require().NoError(err, "NewPTYRelay MUST succeed")
//	defer relay.Cleanup()
//
//	err = relay.Close()
//	s.NoError(err, "First Close() MUST succeed")
//
//	err = relay.Close()
//	s.NoError(err, "Second Close() MUST succeed (idempotent)")
//
//	err = relay.Close()
//	s.NoError(err, "Third Close() MUST succeed (idempotent)")
//}
//
//func (s *PTYRelaySuite) TestIsClosedStateTracking() {
//	// GOAL: Verify IsClosed() correctly tracks state transitions
//	//
//	// TEST SCENARIO: Check state → Close → check state again → state changed to true
//
//	relay, err := NewPTYRelay(s.logger)
//	s.Require().NoError(err, "NewPTYRelay MUST succeed")
//	defer relay.Cleanup()
//
//	s.False(relay.IsClosed(), "IsClosed MUST return false before Close()")
//
//	err = relay.Close()
//	s.Require().NoError(err, "Close() MUST succeed")
//
//	s.True(relay.IsClosed(), "IsClosed MUST return true after Close()")
//}
//
//func (s *PTYRelaySuite) TestCleanupSafe() {
//	// GOAL: Verify Cleanup() can be called safely without panic
//	//
//	// TEST SCENARIO: Create relay → Cleanup → Cleanup again → no panic
//
//	relay, err := NewPTYRelay(s.logger)
//	s.Require().NoError(err, "NewPTYRelay MUST succeed")
//
//	// First cleanup - should work
//	relay.Cleanup()
//
//	// Second cleanup - should not panic
//	relay.Cleanup()
//}
//
//func TestPTYRelaySuite(t *testing.T) {
//	suite.Run(t, new(PTYRelaySuite))
//}
