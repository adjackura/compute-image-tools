//  Copyright 2019 Google Inc. All Rights Reserved.
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
	"fmt"
	"strings"
	"sync"

	osconfig "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/cloud.google.com/go/osconfig/apiv1alpha1"
	"google.golang.org/api/option"

	osconfigpb "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/google.golang.org/genproto/googleapis/cloud/osconfig/v1alpha1"
)

var (
	patchJobCache struct {
		exists map[string]map[string][]string
		mu     sync.Mutex
	}
)

// PatchJob is used to act on an osconfig PatchJob.
type PatchJob struct {
	// The name of this patch job to be used as a reference in this workflow.
	Name string
	*osconfigpb.ExecutePatchJobRequest

	patchJob *osconfigpb.PatchJob
	creator  *Step
	users    []*Step
}

func (j *PatchJob) populate(ctx context.Context, s *Step) dErr {
	// We only populate the client once and only if a ExecutePatchJob step has been defined.
	if s.w.osconfigClient == nil {
		var opts []option.ClientOption
		if s.w.OAuthPath != "" {
			opts = append(opts, option.WithCredentialsFile(s.w.OAuthPath))
		}

		var err error
		s.w.osconfigClient, err = osconfig.NewClient(ctx, opts...)
		if err != nil {
			// Make this non nil so any other PatchJob populate don't throw the same error.
			s.w.osconfigClient = &osconfig.Client{}
			return newErr(err)
		}
	}

	j.Parent = strOr(j.Parent, s.w.Project)
	if !strings.HasPrefix(j.Parent, "projects/") {
		j.Parent = "projects/" + j.Parent
	}
	j.Description = strOr(j.Description, fmt.Sprintf("PatchJob created by Daisy in workflow %q on behalf of %s.", s.w.Name, s.w.username))
	return nil
}

func (j *PatchJob) validate(ctx context.Context, s *Step) dErr {
	if exists, err := projectExists(s.w.ComputeClient, j.Parent); err != nil {
		return errf("cannot create PatchJob %q: bad project lookup: %q, error: %v", j.Name, j.Parent, err)
	} else if !exists {
		return errf("cannot create PatchJob %q: project does not exist: %q", j.Name, j.Parent)
	}
	// Register creation.
	return s.w.patchJobs.regCreate(j.Name, j, s)
}

type patchJobRegistry struct {
	w  *Workflow
	m  map[string]*PatchJob
	mx sync.Mutex
}

func newPatchJobRegistry(w *Workflow) *patchJobRegistry {
	return &patchJobRegistry{w: w, m: map[string]*PatchJob{}}
}

func (r *patchJobRegistry) setJob(name string, j *osconfigpb.PatchJob) dErr {
	r.mx.Lock()
	defer r.mx.Unlock()
	pj, ok := r.m[name]
	if !ok {
		return errf("missing reference for PatchJob %q", name)
	}
	pj.patchJob = j

	return nil
}

func (r *patchJobRegistry) getJobName(name string) (string, dErr) {
	r.mx.Lock()
	defer r.mx.Unlock()
	pj, ok := r.m[name]
	if !ok {
		return "", errf("missing reference for PatchJob %q", name)
	}
	return pj.patchJob.GetName(), nil
}

func (r *patchJobRegistry) regCreate(name string, pj *PatchJob, s *Step) dErr {
	r.mx.Lock()
	defer r.mx.Unlock()
	if pj, ok := r.m[name]; ok {
		return errf("cannot create PatchJob %q; already created by step %q", name, pj.creator.name)
	}

	pj.creator = s
	r.m[name] = pj
	return nil
}

func (r *patchJobRegistry) regUse(name string, s *Step) dErr {
	r.mx.Lock()
	defer r.mx.Unlock()
	pj, ok := r.m[name]
	if !ok {
		return errf("missing reference for PatchJob %q", name)
	}

	if pj.creator != nil && !s.nestedDepends(pj.creator) {
		return errf("using PatchJob %q MUST transitively depend on step %q which creates %q", name, pj.creator.name, name)
	}

	r.m[name].users = append(r.m[name].users, s)
	return nil
}
