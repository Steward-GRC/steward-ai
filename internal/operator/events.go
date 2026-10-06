// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/airules"
)

// The completion events go to their own topic exchange, with the outcome in
// the routing key so a consumer can bind to one outcome only.
const (
	CompletionExchange  = airules.OperatorJobsCompletionExchange
	RoutingKeySucceeded = airules.OperatorJobSucceededRoutingKey
	RoutingKeyFailed    = airules.OperatorJobFailedRoutingKey
)

// Publisher is the part of a go-rabbitmq publisher the events need.
type Publisher interface {
	Publish(ctx context.Context, routingKey string, body []byte) error
}

// MQEventPublisher is the production EventPublisher.
type MQEventPublisher struct {
	pub Publisher
}

// NewMQEventPublisher publishes through pub, which is bound to
// CompletionExchange.
func NewMQEventPublisher(pub Publisher) *MQEventPublisher {
	return &MQEventPublisher{pub: pub}
}

// PublishJobCompletion publishes ev as JSON under the routing key for its
// phase.
func (p *MQEventPublisher) PublishJobCompletion(ctx context.Context, ev CompletionEvent) error {
	routingKey := RoutingKeySucceeded
	if ev.Phase == string(v1alpha1.PhaseFailed) {
		routingKey = RoutingKeyFailed
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("operator: marshal completion event: %w", err)
	}
	if err := p.pub.Publish(ctx, routingKey, body); err != nil {
		return fmt.Errorf("operator: publish completion event: %w", err)
	}
	return nil
}
