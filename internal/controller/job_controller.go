/*
Copyright 2026.

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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
)

// JobReconciler investigates failed batch Jobs and records Obituaries.
type JobReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Collector         *forensics.Collector
	Narrator          forensics.Narrator
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch

// Reconcile checks whether a Job has failed and, if so, records an Obituary.
func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var job batchv1.Job
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cfg := resolveConfig(ctx, r.Client, job.Namespace, r.OperatorNamespace)
	if cfg.Watch.JobFailures == nil || !*cfg.Watch.JobFailures {
		return ctrl.Result{}, nil
	}
	if !namespaceWatched(ctx, r.Client, cfg, job.Namespace) {
		return ctrl.Result{}, nil
	}

	if !jobFailed(&job) {
		return ctrl.Result{}, nil
	}

	owners := r.Collector.OwnerChain(ctx, job.Namespace, job.OwnerReferences)
	events, err := r.Collector.Events(ctx, job.Namespace, string(job.UID))
	if err != nil {
		log.Error(err, "collecting events")
	}

	verdict := forensics.Diagnose(forensics.Evidence{JobFailed: true, Events: events})
	subject := jobSubject(&job, owners)
	key := issueKey(subject, "", verdict.Cause)
	// Each Job run has a unique UID, so it is its own episode; repeated runs of
	// a CronJob roll up into one obituary with an incrementing occurrence count.
	episode := string(job.UID)

	name := obituaryName(subject.Name, key)
	var existing koronerv1alpha1.Obituary
	if gerr := r.Get(ctx, client.ObjectKey{Namespace: subject.Namespace, Name: name}, &existing); gerr == nil {
		if existing.Status.LastEpisode == episode {
			return ctrl.Result{}, nil
		}
	} else if !apierrors.IsNotFound(gerr) {
		return ctrl.Result{}, gerr
	}

	log.Info("recording job failure", "subject", subject.Kind+"/"+subject.Name, "job", job.Name)

	status := koronerv1alpha1.ObituaryStatus{
		Phase:         koronerv1alpha1.PhaseComplete,
		CauseOfDeath:  verdict.Cause,
		Confidence:    verdict.Confidence,
		Summary:       failureSummary(&job, verdict.Summary),
		EventTimeline: events,
		Owners:        owners,
	}
	narrator := resolveNarrator(ctx, r.Client, cfg.Narrator, job.Namespace, r.Narrator)
	runNarrator(ctx, narrator, verdict, &status)

	_, _, err = upsertObituary(ctx, r.Client, recordedDeath{
		subject:  subject,
		issueKey: key,
		episode:  episode,
		lastPod:  job.Name,
		now:      metav1.Now(),
		status:   status,
	})
	return ctrl.Result{}, err
}

// jobSubject groups a Job under its owning workload (e.g. a CronJob) when one
// exists, otherwise the Job itself.
func jobSubject(job *batchv1.Job, owners []koronerv1alpha1.OwnerRecord) koronerv1alpha1.Subject {
	if top := forensics.TopOwner(owners); top.Name != "" {
		return koronerv1alpha1.Subject{
			APIVersion: top.APIVersion,
			Kind:       top.Kind,
			Name:       top.Name,
			Namespace:  job.Namespace,
		}
	}
	return koronerv1alpha1.Subject{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       job.Name,
		Namespace:  job.Namespace,
		UID:        string(job.UID),
	}
}

// jobFailed reports whether a Job has terminally failed.
func jobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// failureSummary enriches the generic verdict with the Job's own failure
// message when it provides one.
func failureSummary(job *batchv1.Job, fallback string) string {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Message != "" {
			return c.Message
		}
	}
	return fallback
}

// SetupWithManager wires the reconciler to watch Jobs.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Narrator == nil {
		r.Narrator = forensics.NoopNarrator{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Named("job").
		Complete(r)
}
