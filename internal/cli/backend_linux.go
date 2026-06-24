//go:build linux

package cli

import (
	"nft-okboy/internal/config"
	"nft-okboy/internal/firewall"
)

// newBackend builds the production nftables backend on Linux. It is wired from
// the config's nft_* knobs (table/chain/priority) and the shared rule prefix.
// EnsureBase is the caller's responsibility (serve calls it; the management
// commands tolerate its absence on a host that is not the firewall itself).
func newBackend(cfg *config.Config) (firewall.FirewallBackend, error) {
	return firewall.NewNftBackend(firewall.NftConfig{
		Prefix:   cfg.RulePrefix,
		Table:    cfg.NftTable,
		Chain:    cfg.NftChain,
		Priority: cfg.NftPriority,
	})
}
