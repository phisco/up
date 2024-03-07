// Copyright 2021 Upbound Inc
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

package controlplane

import (
	"context"
	"time"

	"github.com/alecthomas/kong"
	"github.com/posener/complete"
	"github.com/upbound/up/internal/upterm"
	"k8s.io/apimachinery/pkg/util/duration"

	cp "github.com/upbound/up-sdk-go/service/controlplanes"
	"github.com/upbound/up/cmd/up/controlplane/connector"
	"github.com/upbound/up/cmd/up/controlplane/kubeconfig"
	"github.com/upbound/up/cmd/up/controlplane/pkg"
	"github.com/upbound/up/cmd/up/controlplane/pullsecret"
	"github.com/upbound/up/internal/controlplane"
	"github.com/upbound/up/internal/feature"
	"github.com/upbound/up/internal/upbound"
)

var (
	cloudfieldNames = []string{"NAME", "CONFIGURATION", "UPDATED", "SYNCED", "READY", "MESSAGE", "AGE"}
	spacefieldNames = []string{"GROUP", "NAME", "CROSSPLANE", "SYNCED", "READY", "MESSAGE", "AGE"}
)

// BeforeReset is the first hook to run.
func (c *Cmd) BeforeReset(p *kong.Path, maturity feature.Maturity) error {
	return feature.HideMaturity(p, maturity)
}

// AfterApply constructs and binds a control plane client to any subcommands
// that have Run() methods that receive it.
func (c *Cmd) AfterApply(kongCtx *kong.Context) error {
	upCtx, err := upbound.NewFromFlags(c.Flags)
	if err != nil {
		return err
	}
	kongCtx.Bind(upCtx)

	return nil
}

func PredictControlPlanes() complete.Predictor {
	return complete.PredictFunc(func(a complete.Args) (prediction []string) {
		upCtx, err := upbound.NewFromFlags(upbound.Flags{})
		if err != nil {
			return nil
		}
		cfg, err := upCtx.BuildSDKConfig()
		if err != nil {
			return nil
		}

		cp := cp.NewClient(cfg)
		if cp == nil {
			return nil
		}

		ctps, err := cp.List(context.Background(), upCtx.Account)
		if err != nil {
			return nil
		}

		if len(ctps.ControlPlanes) == 0 {
			return nil
		}

		data := make([]string, len(ctps.ControlPlanes))
		for i, ctp := range ctps.ControlPlanes {
			data[i] = ctp.ControlPlane.Name
		}
		return data
	})
}

// Cmd contains commands for interacting with control planes.
type Cmd struct {
	Connect    connectCmd    `cmd:"" help:"Connect kubectl to control plane."`
	Disconnect disconnectCmd `cmd:"" help:"Disconnect kubectl from control plane."`
	Create     createCmd     `cmd:"" help:"Create a managed control plane."`
	Delete     deleteCmd     `cmd:"" help:"Delete a control plane."`
	List       listCmd       `cmd:"" help:"List control planes for the account."`
	Get        getCmd        `cmd:"" help:"Get a single control plane."`

	Connector connector.Cmd `cmd:"" help:"Connect an App Cluster to a managed control plane."`

	Configuration pkg.Cmd `cmd:"" set:"package_type=Configuration" help:"Manage Configurations."`
	Provider      pkg.Cmd `cmd:"" set:"package_type=Provider" help:"Manage Providers."`

	PullSecret pullsecret.Cmd `cmd:"" help:"Manage package pull secrets."`

	Kubeconfig kubeconfig.Cmd `cmd:"" name:"kubeconfig" help:"Manage control plane kubeconfig data."`

	// Common Upbound API configuration
	Flags upbound.Flags `embed:""`
}

func (c *Cmd) Help() string {
	return `
Interact with control planes of the current profile. Both Upbound profiles and
local Spaces are supported. Use the "profile" management command to switch
between different Upbound profiles or to connect to a local Space.`
}

func extractCloudFields(obj any) []string {
	resp, ok := obj.(*controlplane.Response)
	if !ok {
		return []string{"unknown", "unknown", "", "", "", "", ""}
	}

	return []string{
		resp.Name,
		resp.Cfg,
		resp.Updated,
		resp.Synced,
		resp.Ready,
		resp.Message,
		formatAge(resp.Age),
	}
}

func extractSpaceFields(obj any) []string {
	resp, ok := obj.(*controlplane.Response)
	if !ok {
		return []string{"unknown", "unknown", "", "", "", "", ""}
	}

	return []string{
		resp.Group,
		resp.Name,
		resp.CrossplaneVersion,
		resp.Synced,
		resp.Ready,
		resp.Message,
		formatAge(resp.Age),
	}
}

func formatAge(age *time.Duration) string {
	if age == nil {
		return ""
	}

	return duration.HumanDuration(*age)
}

func tabularPrint(obj any, printer upterm.ObjectPrinter, upCtx *upbound.Context) error {
	if upCtx.Profile.IsSpace() {
		return printer.Print(obj, spacefieldNames, extractSpaceFields)
	}
	return printer.Print(obj, cloudfieldNames, extractCloudFields)
}
