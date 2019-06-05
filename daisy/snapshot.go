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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	daisyCompute "github.com/GoogleCloudPlatform/compute-image-tools/daisy/compute"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

var (
	snapshotCache struct {
		exists map[string][]*compute.Snapshot
		mu     sync.Mutex
	}
	snapshotURLRgx = regexp.MustCompile(fmt.Sprintf(`^(projects/(?P<project>%[1]s)/)?global/snapshots\/(?P<snapshot>%[2]s)$`, projectRgxStr, rfc1035))
)

// Snapshot is a GCE disk snapshot.
type Snapshot struct {
	compute.Snapshot
	GuestFlush bool
	// Should an existing image of the same name be deleted, defaults to false
	// which will fail validation.
	OverWrite bool `json:",omitempty"`

	Resource
}

// MarshalJSON is a hacky workaround to prevent Snapshot from using compute.Snapshot's implementation.
func (s *Snapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(*s)
}

func (s *Snapshot) populate(ctx context.Context, st *Step) dErr {
	var errs dErr
	s.Name, errs = s.Resource.populateWithGlobal(ctx, st, s.Name)

	s.Description = strOr(s.Description, fmt.Sprintf("Snapshot created by Daisy in workflow %q on behalf of %s.", st.w.Name, st.w.username))

	if diskURLRgx.MatchString(s.SourceDisk) {
		s.SourceDisk = extendPartialURL(s.SourceDisk, st.w.Project)
	}

	s.link = fmt.Sprintf("projects/%s/global/snapshots/%s", st.w.Project, s.Name)
	return errs
}

func (s *Snapshot) validate(ctx context.Context, st *Step) dErr {
	pre := fmt.Sprintf("cannot create snapshot %q", s.daisyName)
	errs := s.Resource.validate(ctx, st, pre)

	// Source disk checking.
	if s.SourceDisk == "" {
		return addErrs(errs, errf("%s: must provide SourceDisk", pre))
	}

	if _, err := st.w.disks.regUse(s.SourceDisk, st); err != nil {
		return addErrs(errs, newErr(err))
	}

	// Register snapshot creation.
	errs = addErrs(errs, st.w.snapshots.regCreate(s.daisyName, &s.Resource, st, s.OverWrite))
	return errs
}

// snapshotExists should only be used during validation for existing GCE snapshots
// and should not be relied or populated for daisy created resources.
func snapshotExists(client daisyCompute.Client, project, name string) (bool, dErr) {
	if name == "" {
		return false, errf("must provide snapshot name")
	}
	snapshotCache.mu.Lock()
	defer snapshotCache.mu.Unlock()
	if snapshotCache.exists == nil {
		snapshotCache.exists = map[string][]*compute.Snapshot{}
	}
	if _, ok := snapshotCache.exists[project]; !ok {
		sl, err := client.ListSnapshots(project)
		if err != nil {
			return false, errf("error listing images for project %q: %v", project, err)
		}
		snapshotCache.exists[project] = sl
	}

	for _, i := range snapshotCache.exists[project] {
		if name == i.Name {
			return true, nil
		}
	}

	return false, nil
}

type snapshotRegistry struct {
	baseResourceRegistry
}

func newSnapshotRegistry(w *Workflow) *snapshotRegistry {
	ir := &snapshotRegistry{baseResourceRegistry: baseResourceRegistry{w: w, typeName: "snapshot", urlRgx: imageURLRgx}}
	ir.baseResourceRegistry.deleteFn = ir.deleteFn
	ir.init()
	return ir
}

func (sr *snapshotRegistry) deleteFn(res *Resource) dErr {
	m := namedSubexp(snapshotURLRgx, res.link)
	err := sr.w.ComputeClient.DeleteSnapshot(m["project"], m["snapshot"])
	if gErr, ok := err.(*googleapi.Error); ok && gErr.Code == http.StatusNotFound {
		return typedErr(resourceDNEError, err)
	}
	return newErr(err)
}
