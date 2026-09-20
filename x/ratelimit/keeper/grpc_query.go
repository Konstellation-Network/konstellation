package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct{ k Keeper }

// NewQueryServerImpl returns the Query service implementation.
func NewQueryServerImpl(k Keeper) types.QueryServer { return queryServer{k} }

func (q queryServer) RateLimits(ctx context.Context, _ *types.QueryRateLimitsRequest) (*types.QueryRateLimitsResponse, error) {
	all, err := q.k.AllRateLimits(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRateLimitsResponse{RateLimits: all}, nil
}

func (q queryServer) RateLimit(ctx context.Context, req *types.QueryRateLimitRequest) (*types.QueryRateLimitResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	rl, found, err := q.k.GetRateLimit(ctx, req.Denom, req.ChannelId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "no rate limit for %s on %s", req.Denom, req.ChannelId)
	}
	return &types.QueryRateLimitResponse{RateLimit: &rl}, nil
}

func (q queryServer) RateLimitsByChannel(ctx context.Context, req *types.QueryRateLimitsByChannelRequest) (*types.QueryRateLimitsByChannelResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	out, err := q.k.RateLimitsByChannel(ctx, req.ChannelId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryRateLimitsByChannelResponse{RateLimits: out}, nil
}
