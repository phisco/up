// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package exporter

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/upbound/up/pkg/migration/category"
	"github.com/upbound/up/pkg/migration/meta/v1alpha1"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	xpmeta "github.com/crossplane/crossplane-runtime/pkg/meta"
)

// Options for the exporter.
type Options struct {
	// OutputArchive is the path to the archive file to be created.
	OutputArchive string // default: xp-state.tar.gz

	// Namespaces to include in the export. If not specified, all namespaces are included.
	IncludeNamespaces []string // default: none
	// Namespaces to exclude from the export.
	ExcludeNamespaces []string // default: except kube-system, kube-public, kube-node-lease, local-path-storage

	// Extra resource types to include in the export.
	IncludeExtraResources []string // default: namespaces, configmaps, secrets ( + all Crossplane resources)
	// Resource types to exclude from the export.
	ExcludeResources []string // default: none

	// PauseBeforeExport pauses all managed resources before starting the export process.
	PauseBeforeExport bool // default: false
}

// ControlPlaneStateExporter exports the state of a Crossplane control plane.
type ControlPlaneStateExporter struct {
	crdClient       apiextensionsclientset.Interface
	dynamicClient   dynamic.Interface
	discoveryClient discovery.DiscoveryInterface
	appsClient      appsv1.AppsV1Interface
	resourceMapper  meta.RESTMapper

	options Options
}

// NewControlPlaneStateExporter returns a new ControlPlaneStateExporter.
func NewControlPlaneStateExporter(crdClient apiextensionsclientset.Interface, dynamicClient dynamic.Interface, discoveryClient discovery.DiscoveryInterface, appsClient appsv1.AppsV1Interface, mapper meta.RESTMapper, opts Options) *ControlPlaneStateExporter {
	return &ControlPlaneStateExporter{
		crdClient:       crdClient,
		dynamicClient:   dynamicClient,
		discoveryClient: discoveryClient,
		appsClient:      appsClient,
		resourceMapper:  mapper,

		options: opts,
	}
}

// Export exports the state of the control plane.
func (e *ControlPlaneStateExporter) Export(ctx context.Context) error { // nolint:gocyclo // This is the high level export command, so it's expected to be a bit complex.

	// TODO(turkenh): Check if we can use `afero.NewMemMapFs()` just like import and avoid the need for a temporary directory.
	fs := afero.Afero{Fs: afero.NewOsFs()}
	// We are using a temporary directory to store the exported state before
	// archiving it. This temporary directory will be deleted after the archive
	// is created.
	tmpDir, err := fs.TempDir("", "up")
	if err != nil {
		return errors.Wrap(err, "cannot create temporary directory")
	}
	defer func() {
		_ = fs.RemoveAll(tmpDir)
	}()

	if e.options.PauseBeforeExport {
		cm := category.NewAPICategoryModifier(e.dynamicClient, e.discoveryClient)

		// Modify all managed resources to add the "crossplane.io/paused: true" annotation.
		_, err := cm.ModifyResources(ctx, "managed", func(u *unstructured.Unstructured) error {
			xpmeta.AddAnnotations(u, map[string]string{"crossplane.io/paused": "true"})
			return nil
		})
		if err != nil {
			return errors.Wrap(err, "cannot pause managed resources")
		}
	}

	// Scan the control plane for types to export.
	crdList, err := fetchAllCRDs(ctx, e.crdClient)
	if err != nil {
		return errors.Wrap(err, "cannot fetch CRDs")
	}
	exportList := make([]apiextensionsv1.CustomResourceDefinition, 0, len(crdList))
	for _, crd := range crdList {
		// We only want to export the following types:
		// - Crossplane Core CRDs - Has suffix ".crossplane.io".
		// - CRDs owned by Crossplane packages - Has owner reference to a Crossplane package.
		// - CRDs owned by a CompositeResourceDefinition - Has owner reference to a CompositeResourceDefinition.
		// - Included extra resources - Specified by the user.
		if !e.shouldExport(crd) {
			// Ignore CRDs that we don't want to export.
			continue
		}
		exportList = append(exportList, crd)
	}
	//////////////////////

	// Export Crossplane resources.
	crCounts := make(map[string]int, len(exportList))
	for _, crd := range exportList {
		gvr, err := e.customResourceGVR(crd)
		if err != nil {
			return errors.Wrapf(err, "cannot get GVR for %q", crd.GetName())
		}

		sub := false
		for _, vr := range crd.Spec.Versions {
			if vr.Storage && vr.Subresources != nil && vr.Subresources.Status != nil {
				// This CRD has a status subresource. We store this as a metadata per type and use
				// it during import to determine if we should apply the status subresource.
				sub = true
				break
			}
		}
		exporter := NewUnstructuredExporter(
			NewUnstructuredFetcher(e.dynamicClient, e.options),
			NewFileSystemPersister(fs, tmpDir, &v1alpha1.TypeMeta{
				Categories:            crd.Spec.Names.Categories,
				WithStatusSubresource: sub,
			}))

		// ExportResource will fetch all resources of the given GVR and store them in the
		// well-known directory structure.
		count, err := exporter.ExportResources(ctx, gvr)
		if err != nil {
			return errors.Wrapf(err, "cannot export resources for %q", crd.GetName())
		}
		crCounts[gvr.GroupResource().String()] = count
	}

	total := 0
	for _, count := range crCounts {
		total += count
	}
	//////////////////////

	// Export native resources.
	nativeCounts := make(map[string]int, len(e.options.IncludeExtraResources))

	// In addition to the Crossplane resources, we also need to export some native resources. These are
	// defaulted as "namespaces", "configmaps" and "secrets". However, the user can also specify additional
	// resources to include or exclude the default ones.
	for r := range e.extraResources() {
		gvr, err := e.resourceMapper.ResourceFor(schema.ParseGroupResource(r).WithVersion(""))
		if err != nil {
			return errors.Wrapf(err, "cannot get GVR for %q", r)
		}
		exporter := NewUnstructuredExporter(
			NewUnstructuredFetcher(e.dynamicClient, e.options),
			NewFileSystemPersister(fs, tmpDir, nil))

		count, err := exporter.ExportResources(ctx, gvr)
		if err != nil {
			return errors.Wrapf(err, "cannot export resources for %q", r)
		}
		nativeCounts[gvr.Resource] = count
	}
	total = 0
	for _, count := range nativeCounts {
		total += count
	}
	//////////////////////

	// Export a top level metadata file. This file contains details like when the export was done,
	// the version and feature flags of Crossplane and number of resources exported per type.
	// This metadata file is used during import to determine if the import is compatible with the
	// current Crossplane version and feature flags and also enables manual inspection the exported state.
	me := NewPersistentMetadataExporter(e.appsClient, fs, tmpDir)
	if err = me.ExportMetadata(ctx, e.options, nativeCounts, crCounts); err != nil {
		return errors.Wrap(err, "cannot write export metadata")
	}
	//////////////////////

	// Archive the exported state.
	if err = e.archive(ctx, fs, tmpDir); err != nil {
		return errors.Wrap(err, "cannot archive exported state")
	}
	//////////////////////

	pterm.Println("\nSuccessfully exported control plane state!")
	return nil
}

func (e *ControlPlaneStateExporter) IncludedExtraResource(gr string) bool {
	for r := range e.extraResources() {
		if gr == r {
			return true
		}
	}

	return false
}

func (e *ControlPlaneStateExporter) shouldExport(in apiextensionsv1.CustomResourceDefinition) bool {
	for _, ref := range in.GetOwnerReferences() {
		// Types owned by a Crossplane package.
		if ref.APIVersion == "pkg.crossplane.io/v1" {
			// Note: We could also check the kind and ensure it is owned by a
			// Provider, Function or Configuration. However, this should be
			// enough and would be forward compatible if we introduce additional
			// package types.
			return true
		}

		// Types owned by a CompositeResourceDefinition, e.g. CRDs for Claims and CompositeResources.
		if ref.APIVersion == "apiextensions.crossplane.io/v1" && ref.Kind == "CompositeResourceDefinition" {
			return true
		}
	}

	if strings.HasSuffix(in.GetName(), ".crossplane.io") {
		// Covering all built-in Crossplane CRDs.
		return true
	}

	return e.IncludedExtraResource(in.GetName())
}

func (e *ControlPlaneStateExporter) extraResources() map[string]struct{} {
	extra := make(map[string]struct{}, len(e.options.IncludeExtraResources))
	for _, r := range e.options.IncludeExtraResources {
		extra[r] = struct{}{}
	}

	for _, r := range e.options.ExcludeResources {
		delete(extra, r)
	}
	return extra
}

func (e *ControlPlaneStateExporter) customResourceGVR(in apiextensionsv1.CustomResourceDefinition) (schema.GroupVersionResource, error) {
	version := ""
	for _, vr := range in.Spec.Versions {
		if vr.Storage {
			version = vr.Name
		}
	}

	rm, err := e.resourceMapper.RESTMapping(schema.GroupKind{
		Group: in.Spec.Group,
		Kind:  in.Spec.Names.Kind,
	}, version)

	if err != nil {
		return schema.GroupVersionResource{}, errors.Wrapf(err, "cannot get REST mapping for %q", in.GetName())
	}

	return rm.Resource, nil
}

func (e *ControlPlaneStateExporter) archive(ctx context.Context, fs afero.Afero, dir string) error {
	// Create the output file
	out, err := fs.Create(e.options.OutputArchive)
	if err != nil {
		return err
	}
	defer out.Close()

	// Apply the appropriate permissions to the output file
	if err = fs.Chmod(e.options.OutputArchive, 0600); err != nil {
		return err
	}

	// Create a new gzip writer
	gw := gzip.NewWriter(out)
	defer gw.Close()

	// Create a new tar writer
	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Walk the directory and add each file to the tar archive
	err = filepath.Walk(dir, func(file string, fi os.FileInfo, err error) error {
		// Return any errors encountered while walking the directory
		if err != nil {
			return err
		}

		// Open the file
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()

		// Create a new tar header
		header, err := tar.FileInfoHeader(fi, fi.Name())
		if err != nil {
			return err
		}

		// Write the header to the tar archive
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// Copy the file data to the tar archive
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}

		return nil
	})

	// Return any errors encountered while creating the archive
	if err != nil {
		return err
	}

	return nil
}

func fetchAllCRDs(ctx context.Context, kube apiextensionsclientset.Interface) ([]apiextensionsv1.CustomResourceDefinition, error) {
	var crds []apiextensionsv1.CustomResourceDefinition

	continueToken := ""
	for {
		l, err := kube.ApiextensionsV1().CustomResourceDefinitions().List(ctx, v1.ListOptions{
			Limit:    defaultPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return nil, errors.Wrap(err, "cannot list CRDs")
		}
		crds = append(crds, l.Items...)
		continueToken = l.GetContinue()
		if continueToken == "" {
			break
		}
	}

	return crds, nil
}
