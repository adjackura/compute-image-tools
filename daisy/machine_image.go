//  Copyright 2017 Google Inc. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package daisy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	daisyCompute "github.com/GoogleCloudPlatform/compute-image-tools/daisy/compute"
	computeAlpha "google.golang.org/api/compute/v0.alpha"
	"google.golang.org/api/googleapi"
)

var (
	machineImageCache struct {
		exists map[string][]*computeAlpha.MachineImage
		mu     sync.Mutex
	}
	machineImageURLRgx = regexp.MustCompile(fmt.Sprintf(`^(projects/(?P<project>%[1]s)/)?global/machineImages\/(?P<machineImage>%[2]s)$`, projectRgxStr, rfc1035))
)

// machineImageExists should only be used during validation for existing GCE
// machine images and should not be relied or populated for daisy created
// resources.
func machineImageExists(client daisyCompute.Client, project, name string) (bool, dErr) {
	if name == "" {
		return false, errf("must provide name")
	}
	machineImageCache.mu.Lock()
	defer machineImageCache.mu.Unlock()
	if machineImageCache.exists == nil {
		machineImageCache.exists = map[string][]*computeAlpha.MachineImage{}
	}
	if _, ok := machineImageCache.exists[project]; !ok {
		il, err := client.ListMachineImages(project)
		if err != nil {
			return false, errf("error listing images for project %q: %v", project, err)
		}
		machineImageCache.exists[project] = il
	}

	for _, i := range machineImageCache.exists[project] {
		if name == i.Name {
			return true, nil
		}
	}

	return false, nil
}

// MachineImage is used to create a GCE machine image.
type MachineImage struct {
	computeAlpha.MachineImage
	Resource

	// Should an existing image of the same name be deleted, defaults to false
	// which will fail validation.
	OverWrite bool `json:",omitempty"`
}

// MarshalJSON is a hacky workaround to prevent Image from using compute.Image's implementation.
func (i *MachineImage) MarshalJSON() ([]byte, error) {
	return json.Marshal(*i)
}

func (i *MachineImage) populate(ctx context.Context, s *Step) dErr {
	var errs dErr
	i.Name, errs = i.Resource.populateWithGlobal(ctx, s, i.Name)

	i.Description = strOr(i.Description, fmt.Sprintf("Machine image created by Daisy in workflow %q on behalf of %s.", s.w.Name, s.w.username))

	if instanceURLRgx.MatchString(i.SourceInstance) {
		i.SourceInstance = extendPartialURL(i.SourceInstance, i.Project)
	}
	i.link = fmt.Sprintf("projects/%s/global/machineImages/%s", i.Project, i.Name)
	return errs
}

func (i *MachineImage) validate(ctx context.Context, s *Step) dErr {
	pre := fmt.Sprintf("cannot create machine image %q", i.daisyName)
	errs := i.Resource.validate(ctx, s, pre)

	if i.SourceInstance == "" {
		return addErrs(errs, errf("%s: must provide either SourceInstance", pre))
	}

	if _, err := s.w.instances.regUse(i.SourceInstance, s); err != nil {
		return addErrs(errs, newErr(err))
	}

	// Register image creation.
	return addErrs(errs, s.w.machineImages.regCreate(i.daisyName, &i.Resource, s, i.OverWrite))
}

type machineImageRegistry struct {
	baseResourceRegistry
}

func newMachineImageRegistry(w *Workflow) *machineImageRegistry {
	ir := &machineImageRegistry{baseResourceRegistry: baseResourceRegistry{w: w, typeName: "machineImage", urlRgx: imageURLRgx}}
	ir.baseResourceRegistry.deleteFn = ir.deleteFn
	ir.init()
	return ir
}

func (ir *machineImageRegistry) deleteFn(res *Resource) dErr {
	m := namedSubexp(imageURLRgx, res.link)
	err := ir.w.ComputeClient.DeleteMachineImage(m["project"], m["machineImage"])
	if gErr, ok := err.(*googleapi.Error); ok && gErr.Code == http.StatusNotFound {
		return typedErr(resourceDNEError, err)
	}
	return newErr(err)
}
