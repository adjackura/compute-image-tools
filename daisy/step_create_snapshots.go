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
	"sync"

	"google.golang.org/api/googleapi"
)

// CreateSnapshots is a Daisy CreateSnapshots workflow step.
type CreateSnapshots []*Snapshot

func (c *CreateSnapshots) populate(ctx context.Context, st *Step) dErr {
	var errs dErr
	for _, s := range *c {
		errs = addErrs(errs, s.populate(ctx, st))
	}
	return errs
}

func (c *CreateSnapshots) validate(ctx context.Context, st *Step) dErr {
	var errs dErr
	for _, s := range *c {
		errs = addErrs(errs, s.validate(ctx, st))
	}
	return errs
}

func (c *CreateSnapshots) run(ctx context.Context, s *Step) dErr {
	var wg sync.WaitGroup
	w := s.w
	e := make(chan dErr)
	for _, cs := range *c {
		wg.Add(1)
		go func(cs *Snapshot) {
			defer wg.Done()
			// Get source disk link if SourceDisk is a daisy reference to a disk.
			if d, ok := w.disks.get(cs.SourceDisk); ok {
				cs.SourceDisk = d.link
			}

			m := namedSubexp(diskURLRgx, cs.SourceDisk)

			// Delete existing if OverWrite is true.
			if cs.OverWrite {
				// Just try to delete it, a 404 here indicates the image doesn't exist.
				if err := w.ComputeClient.DeleteSnapshot(cs.Project, cs.Name); err != nil {
					if apiErr, ok := err.(*googleapi.Error); !ok || apiErr.Code != 404 {
						e <- errf("error deleting existing image: %v", err)
						return
					}
				}
			}

			w.LogStepInfo(s.name, "CreateSnapshots", "Creating snapshot %q of disk %q.", cs.Name, cs.SourceDisk)
			if err := w.ComputeClient.CreateSnapshot(cs.Project, m["zone"], m["disk"], &cs.Snapshot); err != nil {
				e <- newErr(err)
				return
			}
		}(cs)
	}

	go func() {
		wg.Wait()
		e <- nil
	}()

	select {
	case err := <-e:
		return err
	case <-w.Cancel:
		wg.Wait()
		return nil
	}
}
