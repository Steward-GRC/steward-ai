// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"errors"
	"testing"
)

// TestDecideDelivery_success confirms a clean Handle result acks and never
// requeues.
func TestDecideDelivery_success(t *testing.T) {
	d := DecideDelivery(nil, false)
	if !d.Ack {
		t.Fatal("expected ack on success")
	}
	d = DecideDelivery(nil, true)
	if !d.Ack {
		t.Fatal("expected ack on success even if redelivered")
	}
}

// TestDecideDelivery_firstFailureRequeuesOnce confirms the first failure on
// a message (not yet redelivered) nacks with requeue=true, giving it exactly
// one retry.
func TestDecideDelivery_firstFailureRequeuesOnce(t *testing.T) {
	d := DecideDelivery(errors.New("embed: tei request: context deadline exceeded"), false)
	if d.Ack {
		t.Fatal("expected nack (ack=false) on failure")
	}
	if !d.Requeue {
		t.Fatal("expected requeue=true on first failure")
	}
}

// TestDecideDelivery_redeliveredFailureDrops is the poison-message guard: a
// message that fails again after being redelivered is dropped, not requeued
// forever. A chunk that could never embed once kept the embeddings server in
// an endless requeue loop.
func TestDecideDelivery_redeliveredFailureDrops(t *testing.T) {
	d := DecideDelivery(errors.New("embed: tei request: context deadline exceeded"), true)
	if d.Ack {
		t.Fatal("expected nack (ack=false) on failure")
	}
	if d.Requeue {
		t.Fatal("expected requeue=false on a redelivered failure — must drop, not loop forever")
	}
}
