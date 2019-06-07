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

// Package daisy describes a daisy workflow.
package daisy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
)

// NewLink returns a newly instantiated Link with w as the Workflow.
func NewLink(name string, w *Workflow) *Link {
	return &Link{Name: name, Workflow: linkedWorkflow{w}}
}

// linkedWorkflow allows unmarshalling a JSON string to be used as the path to
// the linked workflow and still make Print work nicely.
type linkedWorkflow struct {
	*Workflow
}

func (l *linkedWorkflow) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("foo")
	}

	w, err := NewFromFile(s)
	if err != nil {
		return err
	}
	l.Workflow = w

	return nil
}

// Link is a single link in a daisy chained workflow.
type Link struct {
	// Name to use for this link and associated workflow.
	Name string
	// Project default for this link, will override what is set in the
	// ChainedWorkflow.
	Project string
	// Zone default for this link, will override what is set in the
	// ChainedWorkflow.
	Zone string
	// GCSPath default for this link, will override what is set in the
	// ChainedWorkflow.
	GCSPath string
	// OAuthPath default for this link, will override what is set in the
	// ChainedWorkflow.
	OAuthPath string

	// Workflow to use for this link, top level fields will be overriden with
	// this Links values.
	Workflow linkedWorkflow
	// Vars to pass to the Workflow.
	Vars map[string]string
	// Link to run on a successful run.
	// If unset the ChainedWorkflow will end on Link completion.
	OnSuccess string
	// Link to run on a failed run.
	// If unset any failure will imediately stop the ChainedWorkflow run.
	OnFailure string

	number    int
	onSuccess *Link
	onFailure *Link
}

// SetOnSuccess sets the onSuccess Link.
func (l *Link) SetOnSuccess(onSuccess *Link) error {
	if l == onSuccess {
		return errors.New("a Link's onSuccess cannot be itself")
	}
	l.onSuccess = onSuccess
	return nil
}

// SetOnFailure sets the onFailure Link.
func (l *Link) SetOnFailure(onFailure *Link) error {
	if l == onFailure {
		return errors.New("a Link's onFailure cannot be itself")
	}
	l.onFailure = onFailure
	return nil
}

func (l *Link) addVar(k, v string) error {
	for wv := range l.Workflow.Vars {
		if k == wv {
			l.Workflow.AddVar(k, v)
			return nil
		}
	}
	return fmt.Errorf("link %q: unknown Var %q", l.Name, k)
}

func (l *Link) run(ctx context.Context) error {
	fmt.Printf("Running workflow link %q\n", l.Name)
	if err := l.Workflow.Run(ctx); err != nil {
		fmt.Print("Workflow ended in failure")
		if l.onFailure != nil {
			fmt.Print(", running OnFailure link.\n")
			return l.onFailure.run(ctx)
		}
		fmt.Print(".\n")
		return err
	}
	fmt.Print("Workflow ended in success")
	if l.onFailure != nil {
		fmt.Print(", running OnSuccess link.\n")
		return l.onSuccess.run(ctx)
	}
	fmt.Print(".\n")
	return nil
}

// ChainedWorkflow is a daisy chained workflow.
type ChainedWorkflow struct {
	// Name for this daisy chained workflow.
	Name string
	// Project default for this chained workflow.
	Project string
	// Zone default for this chained workflow.
	Zone string
	// GCSPath default for this chained workflow.
	GCSPath string
	// OAuthPath default for this chained workflow.
	OAuthPath string

	// A set of Links that make up this ChainedWorkflow.
	Chain []*Link

	entry *Link

	externalLogging bool
}

func (c *ChainedWorkflow) populate(ctx context.Context) error {
	if c.Project == "" {
		return errors.New("Project field must be set in ChainedWorkflow")
	}

	m := map[string]*Link{}
	// Range over Chain once to populate map.
	for i, link := range c.Chain {
		link.number = i + 1

		if link.Name == "" {
			return fmt.Errorf("link entry %d must have a name", link.number)
		}
		if old, ok := m[link.Name]; ok {
			return fmt.Errorf("link entry %d: duplicate name %q with link entry %d", link.number, link.Name, old.number)
		}

		m[link.Name] = link
		if link.number == 1 {
			c.entry = link
		}
	}

	for _, link := range c.Chain {
		if link.Workflow.Workflow == nil {
			return fmt.Errorf("linked workflow %q cannot be blank", c.Name)
		}

		link.Project = strOr(link.Project, c.Project)
		link.Zone = strOr(link.Zone, c.Zone)
		link.GCSPath = strOr(link.GCSPath, c.GCSPath)
		link.OAuthPath = strOr(link.OAuthPath, c.OAuthPath)

		link.Workflow.Name = link.Name
		link.Workflow.Project = link.Project
		link.Workflow.Zone = link.Zone
		link.Workflow.GCSPath = link.GCSPath
		link.Workflow.OAuthPath = link.OAuthPath

		for k, v := range link.Vars {
			if err := link.addVar(k, v); err != nil {
				return err
			}

		}

		link.Workflow.externalLogging = c.externalLogging
		if err := link.Workflow.PopulateClients(ctx); err != nil {
			return fmt.Errorf("link %q: error running PopulateClients: %v", link.Name, err)
		}

		if err := link.Workflow.populate(ctx); err != nil {
			return fmt.Errorf("link %q: error running populate on workflow: %v", link.Name, err)
		}

		if link.OnSuccess != "" {
			onSuccess, ok := m[link.OnSuccess]
			if !ok {
				return fmt.Errorf("OnSuccess link %q does not exist", link.OnSuccess)
			}
			link.SetOnSuccess(onSuccess)
		}

		if link.OnFailure != "" {
			onFailure, ok := m[link.OnFailure]
			if !ok {
				return fmt.Errorf("OnFailure link %q does not exist", link.OnFailure)
			}
			link.SetOnFailure(onFailure)
		}
	}
	return nil
}

// Print populates then pretty prints the chained workflow.
func (c *ChainedWorkflow) Print(ctx context.Context) {
	c.externalLogging = false
	var ret string
	if err := c.populate(ctx); err != nil {
		ret = fmt.Sprintln("Error populating chained workflow for printing:", err)
	}

	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		ret = ret + fmt.Sprintln("Error marshalling chained workflow for printing:", err)
	}
	fmt.Println(string(b))
	if ret != "" {
		fmt.Print(ret)
		os.Exit(1)
	}
}

// Run runs a ChainedWorkflow.
func (c *ChainedWorkflow) Run(ctx context.Context) error {
	c.externalLogging = true
	if err := c.populate(ctx); err != nil {
		return err
	}
	fmt.Printf("Running chained workflow %q\n", c.Name)
	return c.entry.run(ctx)
}

// NewChainedWorkflowFromFile creates a daisy ChainedWorkflow from a JSON file.
func NewChainedWorkflowFromFile(path string) (*ChainedWorkflow, error) {
	ws := NewChainedWorkflow()

	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}

	if err := json.Unmarshal(b, ws); err != nil {
		return nil, JSONError(path, b, err)
	}

	return ws, nil
}

// NewChainedWorkflow creates a daisy ChainedWorkflow.
func NewChainedWorkflow() *ChainedWorkflow {
	return &ChainedWorkflow{}
}
