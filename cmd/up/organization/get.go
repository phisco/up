// Copyright 2022 Upbound Inc
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

package organization

import (
	"context"

	"github.com/alecthomas/kong"
	"github.com/pterm/pterm"
	"github.com/upbound/up/internal/upterm"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	"github.com/upbound/up-sdk-go/service/organizations"

	"github.com/upbound/up/internal/upbound"
)

// AfterApply sets default values in command after assignment and validation.
func (c *getCmd) AfterApply(kongCtx *kong.Context, upCtx *upbound.Context) error {
	kongCtx.Bind(pterm.DefaultTable.WithWriter(kongCtx.Stdout).WithSeparator("   "))
	return nil
}

// getCmd gets a single organization on Upbound.
type getCmd struct {
	Name string `arg:"" required:"" help:"Name of organization." predictor:"orgs"`
}

// Run executes the get command.
func (c *getCmd) Run(printer upterm.ObjectPrinter, oc *organizations.Client, upCtx *upbound.Context) error {

	// The get command accepts a name, but the get API call takes an ID
	// Therefore we get all orgs and find the one the user requested
	orgs, err := oc.List(context.Background())
	if err != nil {
		return err
	}
	for _, o := range orgs {
		if o.Name == c.Name {
			return printer.Print(o, fieldNames, extractFields)
		}
	}
	return errors.Errorf("no organization named %s", c.Name)
}
