package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The REST route for one limit must accept voucher denoms: grpc-gateway
// matches on the decoded path, so a single-segment {denom} would split
// ibc/<hash> on its slash and 404. The proto uses {denom=**} for that.
func TestRateLimitRESTRouteMatchesVoucherDenom(t *testing.T) {
	for _, denom := range []string{"akash", "ibc/27394FB092D2ECCD56123C74F36E4C1F926001CEADA9CA97EA622B25F41E5EB2", "erc20/0xabc"} {
		components := strings.Split("konstellation/ratelimit/v1/rate_limit/channel-0/"+denom, "/")
		params, err := pattern_Query_RateLimit_0.Match(components, "")
		require.NoError(t, err, denom)
		require.Equal(t, "channel-0", params["channel_id"])
		require.Equal(t, denom, params["denom"])
	}
}
