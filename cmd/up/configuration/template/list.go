// Copyright 2023 Upbound Inc
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

package template

import (
	"context"

	"github.com/pterm/pterm"
	"github.com/upbound/up-sdk-go/service/configurations"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
)

var fieldNames = []string{"ID", "DESCRIPTION", "REPO"}

// listCmd lists configuration templates on Upbound.
type listCmd struct{}

// Run executes the list command.
func (c *listCmd) Run(ctx context.Context, printer upterm.ObjectPrinter, p pterm.TextPrinter, cc *configurations.Client, upCtx *upbound.Context) error {
	templateList, err := cc.ListTemplates(ctx)
	if err != nil {
		return err
	}
	if len(templateList.Templates) == 0 {
		p.Printfln("No configuration templates found.")
		return nil
	}
	return printer.Print(templateList.Templates, fieldNames, extractFields)
}

// extractFields helps render the console output by mapping the response to desired fields.
func extractFields(obj any) []string {
	o := obj.(configurations.ConfigurationTemplateReponse)
	return []string{o.ID, o.Name, o.Repo}
}
