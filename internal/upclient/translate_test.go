/*
Copyright 2026 Uptime.com.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package upclient

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/uptime-com/uptime-k8s-operator/api/v1alpha1"
)

func TestToCheckHTTP_ContactGroupsAlwaysPresent(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{name: "nil", in: nil, want: `"contact_groups":[]`},
		{name: "empty", in: []string{}, want: `"contact_groups":[]`},
		{name: "values", in: []string{"Default", "OnCall"}, want: `"contact_groups":["Default","OnCall"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &monitoringv1alpha1.UptimeCheck{
				ObjectMeta: metav1.ObjectMeta{Name: "n", Namespace: "ns"},
				Spec: monitoringv1alpha1.UptimeCheckSpec{
					Type:          monitoringv1alpha1.CheckTypeHTTP,
					ContactGroups: tc.in,
					HTTP:          &monitoringv1alpha1.HTTPSpec{URL: "https://example.com"},
				},
			}
			out, err := ToCheckHTTP(cr)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			body, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("payload missing %q: %s", tc.want, body)
			}
		})
	}
}

func TestToCheckHTTP_TagsNotForced(t *testing.T) {
	cr := &monitoringv1alpha1.UptimeCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "n", Namespace: "ns"},
		Spec: monitoringv1alpha1.UptimeCheckSpec{
			Type: monitoringv1alpha1.CheckTypeHTTP,
			HTTP: &monitoringv1alpha1.HTTPSpec{URL: "https://example.com"},
		},
	}
	out, err := ToCheckHTTP(cr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out.Tags) != 0 {
		t.Fatalf("tags should be empty when none specified, got %v", out.Tags)
	}
}

func TestToCheckHTTP_RequiresHTTPBlock(t *testing.T) {
	cr := &monitoringv1alpha1.UptimeCheck{
		Spec: monitoringv1alpha1.UptimeCheckSpec{Type: monitoringv1alpha1.CheckTypeHTTP},
	}
	if _, err := ToCheckHTTP(cr); err == nil {
		t.Fatal("expected error when spec.http is nil")
	}
}
