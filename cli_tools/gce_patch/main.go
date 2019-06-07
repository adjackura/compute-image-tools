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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/compute-image-tools/daisy"
	daisyCompute "github.com/GoogleCloudPlatform/compute-image-tools/daisy/compute"
	compute "google.golang.org/api/compute/v0.alpha"

	//"google.golang.org/api/compute/v1"

	osconfigpb "github.com/GoogleCloudPlatform/osconfig/_internal/gapi-cloud-osconfig-go/google.golang.org/genproto/googleapis/cloud/osconfig/v1alpha1"
)

var (
	oauth    = flag.String("oauth", "", "path to oauth json file, overrides what is set in workflow")
	rollback = flag.Bool("rollback", false, "rollback")
	gcsPath  = flag.String("gcs_path", "", "GCS bucket to use, overrides what is set in workflow")
	print    = flag.Bool("print", false, "just print")
	run      = flag.String("run", "", "patch groups to run")
)

type PatchGroup struct {
	Name         string
	Project      string
	GCEFilter    string
	GMI          bool
	AutoRollback bool
}

func randString(n int) string {
	gen := rand.New(rand.NewSource(time.Now().UnixNano()))
	letters := "bdghjlmnpqrstvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[gen.Int63()%int64(len(letters))]
	}
	return string(b)
}

func createGMIStep(proj string, il []*compute.Instance) *daisy.CreateMachineImages {
	gmis := []*daisy.MachineImage{}
	for _, i := range il {
		mi := daisy.MachineImage{
			MachineImage: compute.MachineImage{
				Name:           fmt.Sprintf("gce-patch-backup-%s-%s", i.Name, randString(5)),
				SourceInstance: i.SelfLink,
			},
			OverWrite: true,
			Resource: daisy.Resource{
				Project:   proj,
				ExactName: true,
				NoCleanup: true,
			},
		}
		gmis = append(gmis, &mi)
	}

	ret := daisy.CreateMachineImages(gmis)
	return &ret
}

func createPatchSteps(name, proj string, il []*compute.Instance) (*daisy.ExecutePatchJobs, *daisy.AwaitPatchJobs) {
	var filter string
	for i, inst := range il {
		if i == 0 {
			filter = fmt.Sprintf("(id=%d)", inst.Id)
			continue
		}
		filter += fmt.Sprintf(" OR (id=%d)", inst.Id)
	}

	pj := &daisy.ExecutePatchJobs{
		&daisy.PatchJob{
			Name: name,
			ExecutePatchJobRequest: &osconfigpb.ExecutePatchJobRequest{
				Parent: proj,
				Filter: filter,
			},
		},
	}

	aj := &daisy.AwaitPatchJobs{
		&daisy.AwaitPatchJob{
			Name: name,
		},
	}

	return pj, aj
}

func main() {
	flag.Parse()

	d, err := ioutil.ReadFile(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}

	var pgs []PatchGroup
	if err := json.Unmarshal(d, &pgs); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	client, err := daisyCompute.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	wfm := map[string]*daisy.Workflow{}

	if *rollback {
		for _, pg := range pgs {
			if !pg.GMI {
				log.Printf("Can't rollback %q as GMI is set to false", pg.Name)
			}
			wf := daisy.New()
			wf.Name = pg.Name + "-rollback"
			wf.Project = pg.Project
			wf.OAuthPath = *oauth

			il, err := client.AggregatedListInstances(pg.Project, daisyCompute.ListCallOption(daisyCompute.Filter(pg.GCEFilter)))
			if err != nil {
				log.Fatal(err)
			}

			gmil, err := client.ListMachineImages(pg.Project, daisyCompute.ListCallOption(daisyCompute.OrderBy("creationTimestamp desc")))
			if err != nil {
				log.Fatal(err)
			}

			for _, inst := range il {
				var gmi *compute.MachineImage
				for _, i := range gmil {
					if i.SourceInstance == inst.SelfLink {
						gmi = i
						break
					}
				}
				if gmi == nil {
					log.Printf("Instance %q has no GMI's saved.", inst.Name)
					continue
				}

				delete, err := wf.NewStep("delete-" + inst.Name)
				if err != nil {
					log.Fatal(err)
				}
				delete.DeleteResources = &daisy.DeleteResources{Instances: []string{inst.SelfLink}}

				recreate, err := wf.NewStep("recreate-" + inst.Name)
				if err != nil {
					log.Fatal(err)
				}

				newI := &daisy.Instance{
					Instance: compute.Instance{Name: inst.Name, Zone: inst.Zone, SourceMachineImage: gmi.SelfLink},
					Resource: daisy.Resource{ExactName: true},
				}
				recreate.CreateInstances = &daisy.CreateInstances{newI}

				wf.AddDependency(recreate, delete)
			}

			wfm[pg.Name] = wf
		}
	} else {
		for _, pg := range pgs {
			wf := daisy.New()
			wf.Name = pg.Name
			wf.Project = pg.Project
			wf.OAuthPath = *oauth

			il, err := client.AggregatedListInstances(pg.Project, daisyCompute.ListCallOption(daisyCompute.Filter(pg.GCEFilter)))
			if err != nil {
				log.Fatal(err)
			}

			patchStep, err := wf.NewStep("run-patch-" + pg.Name)
			if err != nil {
				log.Fatal(err)
			}
			awaitPatchStep, err := wf.NewStep("wait-for-patch-" + pg.Name)
			if err != nil {
				log.Fatal(err)
			}

			wf.AddDependency(awaitPatchStep, patchStep)

			pjs, apjs := createPatchSteps(pg.Name, pg.Project, il)
			patchStep.ExecutePatchJobs = pjs
			awaitPatchStep.AwaitPatchJobs = apjs

			if pg.GMI {
				gmiStep, err := wf.NewStep("create-gmis-" + pg.Name)
				if err != nil {
					log.Fatal(err)
				}
				gmiStep.CreateMachineImages = createGMIStep(pg.Project, il)
				wf.AddDependency(patchStep, gmiStep)
			}

			wfm[pg.Name] = wf
		}
	}

	if *run == "" {
		for _, wf := range wfm {
			wf.Print(ctx)
		}
		return
	}

	for _, pg := range strings.Split(*run, ",") {
		wf, ok := wfm[pg]
		if !ok {
			log.Fatalf("Bad patch group: %s", pg)
		}
		if *print {
			wf.Print(ctx)
		} else {
			if err := wf.Run(ctx); err != nil {
				log.Fatal(err)
			}
		}
	}
}
