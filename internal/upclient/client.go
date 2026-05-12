/*
Copyright 2026 Uptime.com.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package upclient adapts the public uptime-client-go SDK to operator needs:
// token lookup from a K8s Secret, ownership-tag conventions, and Spec ->
// CheckHTTP translation.
package upclient

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/uptime-com/uptime-client-go/v2/pkg/upapi"
)

// OwnershipTag is attached to every check the operator creates so a tagged
// List() call surfaces only operator-managed resources. Treated as a safety
// net; primary identity is status.remoteCheckID on the CR.
const OwnershipTag = "k8s-operator"

const defaultTokenKey = "token"

// ErrMissingToken is returned when the Secret exists but the configured key
// is empty or absent.
var ErrMissingToken = errors.New("api token secret key is empty or missing")

// NewFromSecret resolves the API token from a Secret and returns a configured
// uptime-client-go API. The namespace is the namespace of the owning CR.
func NewFromSecret(
	ctx context.Context,
	kube client.Client,
	namespace, secretName, secretKey string,
	extraOpts ...upapi.Option,
) (upapi.API, error) {
	if secretKey == "" {
		secretKey = defaultTokenKey
	}

	var secret corev1.Secret
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, &secret); err != nil {
		return nil, fmt.Errorf("fetch token secret %s/%s: %w", namespace, secretName, err)
	}

	token, ok := secret.Data[secretKey]
	if !ok || len(token) == 0 {
		return nil, fmt.Errorf("%w: secret=%s/%s key=%s", ErrMissingToken, namespace, secretName, secretKey)
	}

	opts := append([]upapi.Option{
		upapi.WithToken(string(token)),
		upapi.WithUserAgent("uptime-k8s-operator"),
	}, extraOpts...)

	return upapi.New(opts...)
}
