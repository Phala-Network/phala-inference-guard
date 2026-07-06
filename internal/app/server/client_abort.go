package server

import (
	"context"
	"sync/atomic"
)

const clientClosedRequestStatus = 499

type clientContextKey struct{}
type clientDisconnectState struct {
	clientCtx context.Context
	recorded  atomic.Bool
}

const (
	clientDisconnectPhaseQueue    = "queue"
	clientDisconnectPhaseUpstream = "upstream"
	clientDisconnectPhaseResponse = "response"
)

func (s *proxyServer) recordClientDisconnect(ctx context.Context, phase string, upstreamCancel bool) bool {
	state, clientCtx := clientDisconnectStateFromContext(ctx)
	if clientCtx == nil || clientCtx.Err() == nil {
		return false
	}
	if state != nil && !state.recorded.CompareAndSwap(false, true) {
		return true
	}
	switch phase {
	case clientDisconnectPhaseQueue:
		s.clientDisconnectQueue.Add(1)
	case clientDisconnectPhaseUpstream:
		s.clientDisconnectUpstream.Add(1)
	case clientDisconnectPhaseResponse:
		s.clientDisconnectResponse.Add(1)
	}
	if upstreamCancel {
		s.clientDisconnectCancel.Add(1)
	}
	return true
}

func attachClientContext(ctx context.Context, clientCtx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	if state, _ := clientDisconnectStateFromContext(ctx); state != nil {
		return ctx
	}
	if clientCtx == nil {
		clientCtx = ctx
	}
	return context.WithValue(ctx, clientContextKey{}, &clientDisconnectState{clientCtx: clientCtx})
}

func clientDisconnectStateFromContext(ctx context.Context) (*clientDisconnectState, context.Context) {
	if ctx == nil {
		return nil, nil
	}
	state, ok := ctx.Value(clientContextKey{}).(*clientDisconnectState)
	if ok {
		return state, state.clientCtx
	}
	return nil, ctx
}
