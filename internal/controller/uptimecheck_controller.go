/*
Copyright 2026 Uptime.com.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/uptime-com/uptime-client-go/v2/pkg/upapi"

	monitoringv1alpha1 "github.com/uptime-com/uptime-k8s-operator/api/v1alpha1"
	"github.com/uptime-com/uptime-k8s-operator/internal/upclient"
)

// requeueAfterError is the retry delay for transient failures (network, 5xx).
const requeueAfterError = 30 * time.Second

// UptimeCheckReconciler reconciles a UptimeCheck object.
type UptimeCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NewAPI is the API factory; tests inject a fake. Production uses
	// upclient.NewFromSecret.
	NewAPI func(ctx context.Context, c client.Client, namespace, secret, key, baseURL string) (upapi.API, error)
}

// +kubebuilder:rbac:groups=monitoring.uptime.com,resources=uptimechecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.uptime.com,resources=uptimechecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.uptime.com,resources=uptimechecks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile converges the remote Uptime.com check toward the desired spec.
func (r *UptimeCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr monitoringv1alpha1.UptimeCheck
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&cr, monitoringv1alpha1.Finalizer) {
			return ctrl.Result{}, nil
		}
		api, err := r.apiFor(ctx, &cr)
		if err != nil {
			r.setCondition(&cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionFalse, "TokenUnavailable", err.Error())
			if statusErr := r.Status().Update(ctx, &cr); statusErr != nil {
				log.Error(statusErr, "status update after token error failed")
			}
			return ctrl.Result{RequeueAfter: requeueAfterError}, err
		}
		return r.reconcileDelete(ctx, &cr, api)
	}

	if !controllerutil.ContainsFinalizer(&cr, monitoringv1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&cr, monitoringv1alpha1.Finalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	api, err := r.apiFor(ctx, &cr)
	if err != nil {
		r.setCondition(&cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionFalse, "TokenUnavailable", err.Error())
		if statusErr := r.Status().Update(ctx, &cr); statusErr != nil {
			log.Error(statusErr, "status update after token error failed")
		}
		return ctrl.Result{RequeueAfter: requeueAfterError}, err
	}

	return r.reconcileUpsert(ctx, &cr, api)
}

func (r *UptimeCheckReconciler) reconcileUpsert(
	ctx context.Context,
	cr *monitoringv1alpha1.UptimeCheck,
	api upapi.API,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	desired, err := upclient.ToCheckHTTP(cr)
	if err != nil {
		r.setCondition(cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionFalse, "InvalidSpec", err.Error())
		return ctrl.Result{}, r.Status().Update(ctx, cr)
	}

	var (
		remote *upapi.Check
		action string
	)

	switch {
	case cr.Status.RemoteCheckID != 0:
		remote, err = api.Checks().UpdateHTTP(ctx, upapi.PrimaryKey(cr.Status.RemoteCheckID), desired)
		action = "Updated"
		if isNotFound(err) {
			log.Info("remote check vanished, recreating", "id", cr.Status.RemoteCheckID)
			cr.Status.RemoteCheckID = 0
			remote, err = api.Checks().CreateHTTP(ctx, desired)
			action = "Recreated"
		}
	default:
		remote, err = api.Checks().CreateHTTP(ctx, desired)
		action = "Created"
	}

	if err != nil {
		r.setCondition(cr, monitoringv1alpha1.ConditionTypeSynced, metav1.ConditionFalse, "APIError", err.Error())
		r.setCondition(cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionFalse, "APIError", err.Error())
		if statusErr := r.Status().Update(ctx, cr); statusErr != nil {
			log.Error(statusErr, "status update after API error failed")
		}
		return ctrl.Result{RequeueAfter: requeueAfterError}, err
	}

	cr.Status.RemoteCheckID = remote.PK
	cr.Status.RemoteCheckURL = remote.URL
	cr.Status.ObservedGeneration = cr.Generation
	r.setCondition(cr, monitoringv1alpha1.ConditionTypeSynced, metav1.ConditionTrue, action, fmt.Sprintf("remote check id=%d", remote.PK))
	r.setCondition(cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionTrue, "Healthy", "remote check is in sync")

	return ctrl.Result{}, r.Status().Update(ctx, cr)
}

func (r *UptimeCheckReconciler) reconcileDelete(
	ctx context.Context,
	cr *monitoringv1alpha1.UptimeCheck,
	api upapi.API,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if cr.Status.RemoteCheckID != 0 {
		if err := api.Checks().Delete(ctx, upapi.PrimaryKey(cr.Status.RemoteCheckID)); err != nil && !isNotFound(err) {
			r.setCondition(cr, monitoringv1alpha1.ConditionTypeReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			if statusErr := r.Status().Update(ctx, cr); statusErr != nil {
				log.Error(statusErr, "status update after delete error failed")
			}
			return ctrl.Result{RequeueAfter: requeueAfterError}, err
		}
		log.Info("remote check deleted", "id", cr.Status.RemoteCheckID)
	}

	controllerutil.RemoveFinalizer(cr, monitoringv1alpha1.Finalizer)
	return ctrl.Result{}, r.Update(ctx, cr)
}

func (r *UptimeCheckReconciler) apiFor(ctx context.Context, cr *monitoringv1alpha1.UptimeCheck) (upapi.API, error) {
	return r.NewAPI(
		ctx, r.Client,
		cr.Namespace,
		cr.Spec.APITokenSecretRef.Name,
		cr.Spec.APITokenSecretRef.Key,
		cr.Spec.APIURL,
	)
}

// SetupWithManager sets up the controller with the Manager.
func (r *UptimeCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.NewAPI == nil {
		r.NewAPI = func(ctx context.Context, c client.Client, ns, secret, key, baseURL string) (upapi.API, error) {
			return upclient.NewFromSecret(ctx, c, ns, secret, key, baseURL)
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&monitoringv1alpha1.UptimeCheck{}).
		Named("uptimecheck").
		Complete(r)
}

func (r *UptimeCheckReconciler) setCondition(
	cr *monitoringv1alpha1.UptimeCheck,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.NewTime(time.Now())
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == condType {
			if cr.Status.Conditions[i].Status != status {
				cr.Status.Conditions[i].LastTransitionTime = now
			}
			cr.Status.Conditions[i].Status = status
			cr.Status.Conditions[i].Reason = reason
			cr.Status.Conditions[i].Message = message
			cr.Status.Conditions[i].ObservedGeneration = cr.Generation
			return
		}
	}
	cr.Status.Conditions = append(cr.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: cr.Generation,
	})
}

// isNotFound returns true for 404-equivalent errors from the Uptime.com API
// or the K8s API. The SDK's typed error carries the upstream *http.Response.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	var apiErr *upapi.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return apiErr.Response.StatusCode == http.StatusNotFound
	}
	return false
}
