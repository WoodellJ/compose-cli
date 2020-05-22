/*
	Copyright (c) 2020 Docker Inc.

	Permission is hereby granted, free of charge, to any person
	obtaining a copy of this software and associated documentation
	files (the "Software"), to deal in the Software without
	restriction, including without limitation the rights to use, copy,
	modify, merge, publish, distribute, sublicense, and/or sell copies
	of the Software, and to permit persons to whom the Software is
	furnished to do so, subject to the following conditions:

	The above copyright notice and this permission notice shall be
	included in all copies or substantial portions of the Software.

	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
	EXPRESS OR IMPLIED,
	INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
	IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
	HOLDERS BE LIABLE FOR ANY CLAIM,
	DAMAGES OR OTHER LIABILITY,
	WHETHER IN AN ACTION OF CONTRACT,
	TORT OR OTHERWISE,
	ARISING FROM, OUT OF OR IN CONNECTION WITH
	THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package server

import (
	"context"
	"net"
	"strings"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"github.com/docker/api/client"
	apicontext "github.com/docker/api/context"
	"github.com/docker/api/context/store"
	"github.com/docker/api/server/proxy"
)

// New returns a new GRPC server.
func New(ctx context.Context) *grpc.Server {
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			unaryServerInterceptor(ctx),
			unary,
		),
		grpc.ChainStreamInterceptor(
			grpc.StreamServerInterceptor(stream),
			grpc.StreamServerInterceptor(streamServerInterceptor(ctx)),
		),
	)
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, hs)
	return s
}

//CreateListener creates a listener either on tcp://, or local listener, supporting unix:// for unix socket or npipe:// for named pipes on windows
func CreateListener(address string) (net.Listener, error) {
	if strings.HasPrefix(address, "tcp://") {
		return net.Listen("tcp", strings.TrimPrefix(address, "tcp://"))
	}
	return createLocalListener(address)
}

func unary(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	return grpc_prometheus.UnaryServerInterceptor(ctx, req, info, handler)
}

func stream(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return grpc_prometheus.StreamServerInterceptor(srv, ss, info, handler)
}

// unaryServerInterceptor configures the context and sends it to the next handler
func unaryServerInterceptor(clictx context.Context) func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		currentContext := getContext(ctx)
		configuredCtx, err := configureContext(clictx, currentContext)
		if err != nil {
			return nil, err
		}

		return handler(configuredCtx, req)
	}
}

// streamServerInterceptor configures the context and sends it to the next handler
func streamServerInterceptor(clictx context.Context) func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		currentContext := getContext(ss.Context())
		ctx, err := configureContext(clictx, currentContext)
		if err != nil {
			return err
		}

		return handler(srv, newServerStream(ctx, ss))
	}
}

// getContext returns the current context name sent in the request metadata, it
// returns an empty string if there is no metadata
// not present
func getContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	key, ok := md[apicontext.Key]
	if !ok {
		return ""
	}

	if len(key) == 1 {
		return key[0]
	}

	return ""
}

// configureContext populates the request context with objects the client
// needs: the context store and the api client
func configureContext(ctx context.Context, currentContext string) (context.Context, error) {
	s := store.ContextStore(ctx)
	ctx = store.WithContextStore(ctx, s)
	if currentContext != "" {
		ctx = apicontext.WithCurrentContext(ctx, currentContext)
	}

	c, err := client.New(ctx)
	if err != nil {
		return nil, err
	}

	ctx, err = proxy.WithClient(ctx, c)
	if err != nil {
		return nil, err
	}

	return ctx, nil
}

// A gRPC server stream will only let you get its context but
// there is no way to set a new (augmented context) to the next
// handler (like we do for a unary request). We need to wrap the grpc.ServerSteam
// to be able to set a new context that will be sent to the next stream interceptor.
type contextServerStream struct {
	s   grpc.ServerStream
	ctx context.Context
}

func newServerStream(ctx context.Context, s grpc.ServerStream) grpc.ServerStream {
	return &contextServerStream{
		s:   s,
		ctx: ctx,
	}
}

func (css *contextServerStream) SetHeader(md metadata.MD) error {
	return css.s.SetHeader(md)
}

func (css *contextServerStream) SendHeader(md metadata.MD) error {
	return css.s.SendHeader(md)
}

func (css *contextServerStream) SetTrailer(md metadata.MD) {
	css.s.SetTrailer(md)
}

func (css *contextServerStream) Context() context.Context {
	return css.ctx
}

func (css *contextServerStream) SendMsg(m interface{}) error {
	return css.s.SendMsg(m)
}

func (css *contextServerStream) RecvMsg(m interface{}) error {
	return css.s.RecvMsg(m)
}
