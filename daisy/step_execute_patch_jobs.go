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
)

// ExecutePatchJobs is a Daisy ExecutePatchJobs workflow step.
type ExecutePatchJobs []*PatchJob

func (e *ExecutePatchJobs) populate(ctx context.Context, s *Step) dErr {
	var errs dErr
	for _, j := range *e {
		errs = addErrs(errs, j.populate(ctx, s))
	}

	return errs
}

func (e *ExecutePatchJobs) validate(ctx context.Context, s *Step) dErr {
	var errs dErr
	for _, j := range *e {
		errs = addErrs(errs, j.validate(ctx, s))
	}

	return errs
}

func (e *ExecutePatchJobs) run(ctx context.Context, s *Step) dErr {
	for _, j := range *e {
		job, err := s.w.osconfigClient.ExecutePatchJob(ctx, j.ExecutePatchJobRequest)
		if err != nil {
			return newErr(err)
		}

		j.patchJob = job
	}
	return nil
}
