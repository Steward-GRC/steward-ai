// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
)

// knownDeferred allowlists an RPC still served by the embedded
// UnimplementedAiServiceServer, with the reason. Keep it empty.
var knownDeferred = map[string]string{}

// TestGRPCHandlers_NoUnimplementedRPCs calls every unary method of the
// generated interface on a zero-value handler. A real handler returns or
// panics on a nil dependency (recovered, and a pass); only a method falling
// through to the Unimplemented stub returns codes.Unimplemented.
func TestGRPCHandlers_NoUnimplementedRPCs(t *testing.T) {
	iface := reflect.TypeFor[aiv1.AiServiceServer]()
	handler := reflect.ValueOf(&AiServiceServer{})
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()

	invoked := 0
	for m := range iface.Methods() {
		if m.Type.NumIn() != 2 || m.Type.NumOut() != 2 || m.Type.In(0) != ctxType || m.Type.Out(1) != errType {
			continue
		}
		invoked++
		reason, deferred := knownDeferred[m.Name]
		t.Run(m.Name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil && deferred {
					t.Fatalf("%s panicked (%v) but is listed in knownDeferred; remove the entry", m.Name, r)
				}
			}()
			out := handler.MethodByName(m.Name).Call([]reflect.Value{
				reflect.ValueOf(context.Background()), reflect.New(m.Type.In(1).Elem()),
			})
			err, _ := out[1].Interface().(error)
			if err != nil && status.Code(err) == codes.Unimplemented {
				if deferred {
					t.Logf("%s is unimplemented (allowlisted): %s", m.Name, reason)
					return
				}
				t.Errorf("%s falls through to the Unimplemented stub; implement it or list it in knownDeferred", m.Name)
			}
		})
	}

	// Bump alongside the proto, so a shape change can't zero the coverage.
	const wantInvoked = 20
	if invoked != wantInvoked {
		t.Fatalf("expected %d unary RPCs, exercised %d", wantInvoked, invoked)
	}
	if n := len(aiv1.AiService_ServiceDesc.Methods); n != wantInvoked {
		t.Fatalf("the service descriptor lists %d methods, expected %d", n, wantInvoked)
	}
}
