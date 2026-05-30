/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bootstrap

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// DefaultConfigName is the conventional name for the cluster-wide fallback
// KoronerConfig. Per-namespace configs override it.
const DefaultConfigName = "default"

// DefaultConfigSeeder creates a KoronerConfig named "default" in the operator
// namespace at startup when one is absent, so users have a discoverable,
// editable starting point. If the operator namespace is unknown (e.g. local
// `make run`) the seeder is a no-op.
type DefaultConfigSeeder struct {
	Client    client.Client
	Cache     cache.Cache
	Namespace string
}

var (
	_ manager.Runnable               = (*DefaultConfigSeeder)(nil)
	_ manager.LeaderElectionRunnable = (*DefaultConfigSeeder)(nil)
)

// NeedLeaderElection ensures only one replica seeds the default config when
// multiple operator pods are running.
func (s *DefaultConfigSeeder) NeedLeaderElection() bool { return true }

func (s *DefaultConfigSeeder) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("bootstrap")
	if s.Namespace == "" {
		log.Info("operator namespace unknown; skipping default KoronerConfig seed")
		return nil
	}
	if !s.Cache.WaitForCacheSync(ctx) {
		return ctx.Err()
	}

	key := client.ObjectKey{Namespace: s.Namespace, Name: DefaultConfigName}
	var existing koronerv1alpha1.KoronerConfig
	err := s.Client.Get(ctx, key, &existing)
	switch {
	case err == nil:
		log.V(1).Info("default KoronerConfig already exists", "namespace", s.Namespace)
		return nil
	case !apierrors.IsNotFound(err):
		return err
	}

	cfg := &koronerv1alpha1.KoronerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigName,
			Namespace: s.Namespace,
			Annotations: map[string]string{
				"koroner.pez.sh/auto-generated": "true",
			},
		},
		Spec: koronerv1alpha1.DefaultKoronerConfigSpec(),
	}
	if err := s.Client.Create(ctx, cfg); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	log.Info("seeded default KoronerConfig", "namespace", s.Namespace)
	return nil
}
