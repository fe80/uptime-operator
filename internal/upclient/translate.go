/*
Copyright 2026 Uptime.com.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package upclient

import (
	"fmt"

	"github.com/uptime-com/uptime-client-go/v2/pkg/upapi"

	monitoringv1alpha1 "github.com/uptime-com/uptime-k8s-operator/api/v1alpha1"
)

// ToCheckHTTP converts the operator's UptimeCheck spec into the SDK's
// CheckHTTP payload, injecting the operator's ownership tag.
func ToCheckHTTP(cr *monitoringv1alpha1.UptimeCheck) (upapi.CheckHTTP, error) {
	if cr.Spec.HTTP == nil {
		return upapi.CheckHTTP{}, fmt.Errorf("spec.http is required when type=%s", cr.Spec.Type)
	}

	name := cr.Spec.Name
	if name == "" {
		name = fmt.Sprintf("%s/%s", cr.Namespace, cr.Name)
	}

	paused := cr.Spec.Paused
	tags := mergeTags(OwnershipTag, cr.Spec.Tags)

	out := upapi.CheckHTTP{
		Name:         name,
		Address:      cr.Spec.HTTP.URL,
		Interval:     int64(cr.Spec.Interval),
		Locations:    cr.Spec.Locations,
		Tags:         tags,
		IsPaused:     &paused,
		StatusCode:   cr.Spec.HTTP.StatusCode,
		SendString:   cr.Spec.HTTP.SendString,
		ExpectString: cr.Spec.HTTP.ExpectString,
		Headers:      cr.Spec.HTTP.Headers,
		NumRetries:   int64(cr.Spec.HTTP.NumRetries),
		Threshold:    int64(cr.Spec.HTTP.Threshold),
	}

	if cr.Spec.HTTP.Encryption != "" {
		enc := cr.Spec.HTTP.Encryption
		out.Encryption = &enc
	}

	if len(cr.Spec.ContactGroups) > 0 {
		cg := append([]string(nil), cr.Spec.ContactGroups...)
		out.ContactGroups = &cg
	}

	return out, nil
}

// mergeTags returns required + extra, deduped, with required first.
func mergeTags(required string, extra []string) []string {
	out := []string{required}
	seen := map[string]struct{}{required: {}}
	for _, t := range extra {
		if _, ok := seen[t]; ok || t == "" {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
