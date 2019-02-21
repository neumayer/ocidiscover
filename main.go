// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/kit/log"
	"github.com/neumayer/ocidiscover/oci"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/documentation/examples/custom-sd/adapter"
)

var (
	a                     = kingpin.New("sd adapter usage", "Tool to generate file_sd target files for unimplemented SD mechanisms.")
	outputFile            = a.Flag("output.file", "Output file for file_sd compatible file.").Default("custom_sd.json").String()
	rootCompartmentID     = a.Flag("sd.root_compartment_id", "The ocid of the root compartment for service discovery.").String()
	compartmentID         = a.Flag("sd.compartment_id", "The ocid of the compartment for service discovery.").String()
	port                  = a.Flag("sd.port", "Port for service discovery.").Int()
	displayName           = a.Flag("sd.display_name", "Display name for service discovery.").String()
	useInstancePrincipals = a.Flag("sd.use_instance_principals", "Whether or not to use instance principals for service discovery.").Bool()
	logger                log.Logger
)

func parseConfig() oci.SDConfig {
	if *rootCompartmentID == "" && *compartmentID == "" || *rootCompartmentID != "" && *compartmentID != "" {
		fmt.Println("OCI SD configuration requires either a specific compartment id or the root compartment id (not both)")
		os.Exit(1)
	}
	cfg := oci.SDConfig{}
	if *port != 0 {
		cfg.Port = *port
	}
	if *displayName != "" {
		cfg.DisplayName = *displayName
	}
	if *rootCompartmentID != "" {
		cfg.RootCompartmentID = *rootCompartmentID
	}
	if *compartmentID != "" {
		cfg.CompartmentID = *compartmentID
	}
	cfg.RefreshInterval = model.Duration(60 * time.Second)
	cfg.UseInstancePrincipals = *useInstancePrincipals
	return cfg
}

func main() {
	a.HelpFlag.Short('h')

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Println("err: ", err)
		return
	}
	parseConfig()
	logger = log.NewSyncLogger(log.NewLogfmtLogger(os.Stdout))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)

	ctx := context.Background()

	cfg := parseConfig()
	disc, err := oci.NewDiscovery(cfg, logger)

	if err != nil {
		fmt.Println("err: ", err)
	}
	sdAdapter := adapter.NewAdapter(ctx, *outputFile, "exampleSD", disc, logger)
	sdAdapter.Run()

	<-ctx.Done()
}
