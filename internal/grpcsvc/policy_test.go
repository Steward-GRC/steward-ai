// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	grpcactor "github.com/Bugs5382/go-grpc-actor"
	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/server"
	"github.com/Steward-GRC/steward-ai/internal/workloadauth"
)

func TestCallerPolicy_gatewayOnBehalfOnEveryMethod(t *testing.T) {
	methods := aiv1.AiService_ServiceDesc.Methods
	require.Len(t, CallerPolicy, len(methods), "the policy lists exactly the service's methods")
	for _, m := range methods {
		full := "/" + aiv1.AiService_ServiceDesc.ServiceName + "/" + m.MethodName
		callers, ok := CallerPolicy[full]
		require.True(t, ok, "%s is not in the policy", full)
		require.Equal(t, map[string]workloadauth.Access{"gateway": workloadauth.OnBehalf}, callers, full)
	}
}

// fakeVerifier accepts a token naming a service account; anything else is
// rejected.
type fakeVerifier map[string]workloadauth.Caller

func (f fakeVerifier) Verify(token string) (workloadauth.Caller, error) {
	if c, ok := f[token]; ok {
		return c, nil
	}
	return workloadauth.Caller{}, workloadauth.ErrRejected
}

func serveAI(t *testing.T) (aiv1.AiServiceClient, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	verifier := fakeVerifier{
		"token-gateway":  {Name: "gateway", ServiceAccount: "steward/steward-gateway"},
		"token-delivery": {Name: "delivery", ServiceAccount: "steward/steward-delivery"},
	}
	svc := newFakes().server()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, lis, log.Nop(), server.Options{Auth: &server.Auth{Verifier: verifier, Policy: CallerPolicy}},
			func(s *grpc.Server) { aiv1.RegisterAiServiceServer(s, svc) })
	}()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(grpcactor.UnaryClientInterceptor()))
	require.NoError(t, err)
	return aiv1.NewAiServiceClient(conn), func() {
		_ = conn.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	}
}

func asCaller(token string) context.Context {
	ctx := grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: fixture.Erin})
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func TestCallerPolicy_gatewayIsServed(t *testing.T) {
	client, stop := serveAI(t)
	defer stop()
	_, err := client.GetAIEnabled(asCaller("token-gateway"), &aiv1.GetAIEnabledRequest{})
	require.NoError(t, err)
	resp, err := client.GetAIConfig(asCaller("token-gateway"), &aiv1.GetAIConfigRequest{})
	require.NoError(t, err, "the gateway's forwarded actor is believed")
	require.True(t, resp.GetConfig().GetEnabled())
}

func TestCallerPolicy_refusesAValidTokenFromAnUnlistedCaller(t *testing.T) {
	client, stop := serveAI(t)
	defer stop()
	_, err := client.GetAIEnabled(asCaller("token-delivery"), &aiv1.GetAIEnabledRequest{})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "delivery holds a valid identity but isn't listed")
	_, err = client.SearchAndAnswer(asCaller("token-delivery"), &aiv1.SearchAndAnswerRequest{Question: "q"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = client.GetAIEnabled(asCaller("not-a-token"), &aiv1.GetAIEnabledRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
