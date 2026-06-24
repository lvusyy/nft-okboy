//go:build !linux

package cli

import (
	"nft-okboy/internal/config"
	"nft-okboy/internal/firewall"
)

// newBackend returns the in-memory mock backend on non-Linux hosts (the dev
// boxes where nftables does not exist). Firewall mutations are no-ops that touch
// nothing real, so the management/serve commands stay runnable for development
// and tests without root or an nft binary.
func newBackend(cfg *config.Config) (firewall.FirewallBackend, error) {
	return firewall.NewMockBackend(cfg.RulePrefix), nil
}
