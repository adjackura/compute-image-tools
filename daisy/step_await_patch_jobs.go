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
	"sync"
	"time"

	osconfigpb "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/google.golang.org/genproto/googleapis/cloud/osconfig/v1alpha1"
	"google.golang.org/grpc/status"
)

// AwaitPatchJobs is a Daisy AwaitPatchJob workflow step.
type AwaitPatchJobs []*AwaitPatchJob

// AwaitPatchJob waits for an already executed PatchJob to completed.
type AwaitPatchJob struct {
	// PatchJob name (relative to this workflow) to await.
	Name string
	// Interval to check for signal (default is 5s).
	// Must be parsable by https://golang.org/pkg/time/#ParseDuration.
	Interval string `json:",omitempty"`
	interval time.Duration
}

func (a *AwaitPatchJobs) populate(ctx context.Context, s *Step) dErr {
	var errs dErr
	for _, as := range *a {
		if as.Interval == "" {
			as.Interval = defaultInterval
		}
		var err error
		as.interval, err = time.ParseDuration(as.Interval)
		if err != nil {
			addErrs(errs, err)
		}
	}
	return errs
}

func (a *AwaitPatchJobs) validate(ctx context.Context, s *Step) dErr {
	var errs dErr
	for _, aj := range *a {
		addErrs(errs, s.w.patchJobs.regUse(aj.Name, s))
	}

	return errs
}

func isPatchJobFailureState(state osconfigpb.PatchJob_State) bool {
	return state == osconfigpb.PatchJob_COMPLETED_WITH_ERRORS ||
		state == osconfigpb.PatchJob_TIMED_OUT ||
		state == osconfigpb.PatchJob_CANCELED
}

func getStatusFromError(err error) string {
	if s, ok := status.FromError(err); ok {
		return fmt.Sprintf("code: %q, message: %q, details: %q", s.Code(), s.Message(), s.Details())
	}
	return fmt.Sprintf("%v", err)
}

func (a *AwaitPatchJobs) run(ctx context.Context, s *Step) dErr {
	var wg sync.WaitGroup
	e := make(chan dErr)

	for _, aj := range *a {
		wg.Add(1)
		go func(aj *AwaitPatchJob) {
			defer wg.Done()
			name, err := s.w.patchJobs.getJobName(aj.Name)
			if err != nil {
				e <- err
				return
			}
			s.w.LogStepInfo(s.name, "AwaitPatchJobs", "Awaiting PatchJob %q (daisy name: %q).", name, aj.Name)
			tick := time.Tick(aj.interval)
			for {
				select {
				case <-tick:
					req := &osconfigpb.GetPatchJobRequest{
						Name: name,
					}
					res, err := s.w.osconfigClient.GetPatchJob(ctx, req)
					if err != nil {
						e <- errf("error while fetching patch job %q: %s", name, getStatusFromError(err))
						return
					}
					s.w.patchJobs.setJob(aj.Name, res)

					if isPatchJobFailureState(res.State) {
						e <- errf("failure status %v with message '%s'", res.State, res.GetErrorMessage())
						return
					}

					if res.State == osconfigpb.PatchJob_SUCCEEDED {
						return
					}
				}
			}
		}(aj)
	}

	go func() {
		wg.Wait()
		e <- nil
	}()

	select {
	case err := <-e:
		return err
	case <-s.w.Cancel:
		return nil
	}
}
