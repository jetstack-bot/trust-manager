/*
Copyright 2021 The cert-manager Authors.

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

package internal

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ cache.Cache = &multiScopedCache{}

// multiScopedCache is a controller-runtime cache.Cache that provides informers
// for different scope levels for different resource types, regardless of the
// scope of the resource itself.
// This allows for watching one set of Namespaced resources within a particular
// namespace, whilst the other Namespaced resources in all namespaces.
// It wraps both the default and Namespaced controller-runtime Cache.
type multiScopedCache struct {
	// namespacedInformers is the set of resource types that should only be
	// watched in the namespace pool.
	namespacedInformers []schema.GroupKind
	// namespacedCache watches resources only in a particular namespace.
	namespacedCache cache.Cache

	// clusterCache watches resources in all namespaces.
	clusterCache cache.Cache
}

// NewMultiScopedCache returns a controller-runtime NewCacheFunc that returns a
// cache that allows watching some resources at the cluster level, whilst other
// resources in the given namespace. namespacedInformers is the set of resource
// types which should only be watched in the given namespace.
// namespacedInformers expects Namespaced resource types.
func NewMultiScopedCache(namespace string, namespacedInformers []schema.GroupKind) cache.NewCacheFunc {
	return func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
		namespacedCache, err := cache.New(config, cache.Options{
			Scheme:            opts.Scheme,
			Mapper:            opts.Mapper,
			Namespace:         namespace,
			Resync:            opts.Resync,
			SelectorsByObject: opts.SelectorsByObject,
		})
		if err != nil {
			return nil, err
		}
		clusterCache, err := cache.New(config, opts)
		if err != nil {
			return nil, err
		}
		return &multiScopedCache{
			namespacedCache:     namespacedCache,
			clusterCache:        clusterCache,
			namespacedInformers: namespacedInformers,
		}, nil
	}
}

// GetInformer returns the underlying cache's GetInformer based on resource type.
func (b *multiScopedCache) GetInformer(ctx context.Context, obj client.Object) (cache.Informer, error) {
	return b.cacheFromGVK(obj.GetObjectKind().GroupVersionKind()).GetInformer(ctx, obj)
}

// GetInformerForKind returns the underlying cache's GetInformerForKind based
// on resource type.
func (b *multiScopedCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (cache.Informer, error) {
	return b.cacheFromGVK(gvk).GetInformerForKind(ctx, gvk)
}

// Start starts both the cluster and namespaced caches. Returned is an
// aggregated error from both caches.
func (b *multiScopedCache) Start(ctx context.Context) error {
	var (
		errs []error
		lock sync.Mutex
		wg   sync.WaitGroup
	)

	for _, c := range []cache.Cache{b.namespacedCache, b.clusterCache} {
		wg.Add(1)
		go func(c cache.Cache) {
			if err := c.Start(ctx); err != nil {
				lock.Lock()
				defer lock.Unlock()
				errs = append(errs, err)
			}

			wg.Done()
		}(c)
	}

	wg.Wait()

	return utilerrors.NewAggregate(errs)
}

// WaitForCacheSync will wait for both cluster and namespaced caches to sync.
// Returns false if either cache fails to sync.
func (b *multiScopedCache) WaitForCacheSync(ctx context.Context) bool {
	for _, c := range []cache.Cache{b.namespacedCache, b.clusterCache} {
		if !c.WaitForCacheSync(ctx) {
			return false
		}
	}
	return true
}

// IndexField returns the underlying cache's IndexField based on resource type.
func (b *multiScopedCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return b.cacheFromGVK(obj.GetObjectKind().GroupVersionKind()).IndexField(ctx, obj, field, extractValue)
}

// Get returns the underlying cache's Get based on resource type.
func (b *multiScopedCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return b.cacheFromGVK(obj.GetObjectKind().GroupVersionKind()).Get(ctx, key, obj)
}

// List returns the underlying cache's List based on resource type.
func (b *multiScopedCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return b.cacheFromGVK(list.GetObjectKind().GroupVersionKind()).List(ctx, list, opts...)
}

// cacheFromGVK returns either the cluster or namespaced cache, based on the
// resource type given.
func (b *multiScopedCache) cacheFromGVK(gvk schema.GroupVersionKind) cache.Cache {
	for _, namespacedInformer := range b.namespacedInformers {
		if namespacedInformer.Group == gvk.Group && namespacedInformer.Kind == gvk.Kind {
			return b.namespacedCache
		}
	}
	return b.clusterCache
}
