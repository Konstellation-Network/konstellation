package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/collections"

	"github.com/cosmos/cosmos-sdk/types/query"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct{ k Keeper }

// NewQueryServerImpl returns the Query service implementation.
func NewQueryServerImpl(k Keeper) types.QueryServer { return queryServer{k} }

func (q queryServer) Params(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	return &types.QueryParamsResponse{Params: q.k.GetParams(ctx)}, nil
}

func (q queryServer) Status(ctx context.Context, req *types.QueryStatusRequest) (*types.QueryStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	addr, err := types.ParseAddress(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &types.QueryStatusResponse{
		Verified: q.k.IsVerified(ctx, addr),
		Frozen:   q.k.IsFrozen(ctx, addr),
		Entries:  q.k.Entries(ctx, addr),
	}, nil
}

func (q queryServer) Entries(ctx context.Context, req *types.QueryEntriesRequest) (*types.QueryEntriesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	m, err := q.k.listOf(req.List)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entries, page, err := query.CollectionPaginate(ctx, m, req.Pagination,
		func(_ []byte, e types.ListEntry) (types.ListEntry, error) { return e, nil })
	if err != nil {
		return nil, err
	}
	return &types.QueryEntriesResponse{Entries: entries, Pagination: page}, nil
}

func (q queryServer) PendingUpdates(ctx context.Context, req *types.QueryPendingUpdatesRequest) (*types.QueryPendingUpdatesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	pending, page, err := query.CollectionPaginate(ctx, q.k.Pending, req.Pagination,
		func(_ uint64, p types.PendingUpdate) (types.PendingUpdate, error) { return p, nil })
	if err != nil {
		return nil, err
	}
	return &types.QueryPendingUpdatesResponse{Pending: pending, Pagination: page}, nil
}

func (q queryServer) PendingUpdate(ctx context.Context, req *types.QueryPendingUpdateRequest) (*types.QueryPendingUpdateResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	p, err := q.k.Pending.Get(ctx, req.Id)
	if err != nil {
		if err == collections.ErrNotFound {
			return nil, status.Errorf(codes.NotFound, "pending update %d", req.Id)
		}
		return nil, err
	}
	return &types.QueryPendingUpdateResponse{Pending: p}, nil
}
