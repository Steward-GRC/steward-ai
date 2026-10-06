// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

// DeliveryDecision is what to do with a delivery once Handle has run.
type DeliveryDecision struct {
	Ack bool
	// Requeue only means something when Ack is false.
	Requeue bool
}

// DecideDelivery bounds retries: a success acks, a first failure is requeued
// once, and a failure on redelivery is dropped. A message that can never
// succeed (a chunk the embeddings server rejects, a batch that always times
// out) would otherwise loop forever.
func DecideDelivery(handleErr error, redelivered bool) DeliveryDecision {
	if handleErr == nil {
		return DeliveryDecision{Ack: true}
	}
	if redelivered {
		return DeliveryDecision{Ack: false, Requeue: false}
	}
	return DeliveryDecision{Ack: false, Requeue: true}
}
