package tailnet

import (
	"context"
	"net"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

type watcher interface {
	Next() (ipn.Notify, error)
	Close() error
}

type backend interface {
	Start() error
	Close() error
	Listen(string, string) (net.Listener, error)
	ListenTLS(string, string) (net.Listener, error)
	Status(context.Context) (*ipnstate.Status, error)
	WhoIs(context.Context, string) (*apitype.WhoIsResponse, error)
	Login(context.Context) error
	Logout(context.Context) error
	Rename(context.Context, string) error
	Watch(context.Context) (watcher, error)
}

type nativeBackend struct {
	*tsnet.Server
	client *local.Client
}

func (b *nativeBackend) Start() error {
	if err := b.Server.Start(); err != nil {
		return err
	}
	c, err := b.Server.LocalClient()
	b.client = c
	return err
}

func (b *nativeBackend) Status(ctx context.Context) (*ipnstate.Status, error) {
	return b.client.StatusWithoutPeers(ctx)
}
func (b *nativeBackend) WhoIs(ctx context.Context, addr string) (*apitype.WhoIsResponse, error) {
	return b.client.WhoIs(ctx, addr)
}
func (b *nativeBackend) Login(ctx context.Context) error {
	return b.client.StartLoginInteractive(ctx)
}
func (b *nativeBackend) Logout(ctx context.Context) error { return b.client.Logout(ctx) }
func (b *nativeBackend) Rename(ctx context.Context, hostname string) error {
	_, err := b.client.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{Hostname: hostname}, HostnameSet: true})
	return err
}
func (b *nativeBackend) Watch(ctx context.Context) (watcher, error) {
	return b.client.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialNetMap|ipn.NotifyNoPrivateKeys)
}
