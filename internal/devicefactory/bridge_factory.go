package devicefactory

import (
	"context"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/lua"
	"github.com/srg/blim/internal/ptyio"
)

// BridgeFactory creates bridge components.
// This interface allows tests to override component creation for mocking and error simulation.
type BridgeFactory interface {
	// CreateLuaAPI creates a device, wraps it in LuaAPI, and connects.
	// This is an atomic operation: either returns a fully connected LuaAPI or an error.
	CreateLuaAPI(ctx context.Context, opts *device.ConnectOptions, logger *logrus.Logger) (lua.LuaAPIInterface, error)

	// CreatePTY creates a PTY for bridge I/O.
	CreatePTY(inBuf, outBuf int, logger *logrus.Logger) (ptyio.PTY, error)

	// CreateSymlink creates a symlink to the PTY.
	CreateSymlink(target, linkPath string) error
}

// defaultBridgeFactory is the production implementation of BridgeFactory.
type defaultBridgeFactory struct{}

func (f *defaultBridgeFactory) CreateLuaAPI(ctx context.Context, opts *device.ConnectOptions, logger *logrus.Logger) (lua.LuaAPIInterface, error) {
	dev := NewDevice(opts.Address, logger)
	luaApi := lua.NewBLEAPI2(dev, logger)

	if err := dev.Connect(ctx, opts); err != nil {
		luaApi.Close()
		return nil, err
	}
	return luaApi, nil
}

func (f *defaultBridgeFactory) CreatePTY(inBuf, outBuf int, logger *logrus.Logger) (ptyio.PTY, error) {
	return ptyio.NewPty(inBuf, outBuf, logger)
}

func (f *defaultBridgeFactory) CreateSymlink(target, linkPath string) error {
	return os.Symlink(target, linkPath)
}

// DefaultBridgeFactory is the package-level factory variable.
// Override this in tests to inject a custom factory implementation.
var DefaultBridgeFactory BridgeFactory = &defaultBridgeFactory{}
