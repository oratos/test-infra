/*
Copyright 2018 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// apicoverage.go parses the log file and outputs the api coverage numbers in a
// testgrid expected output xml file

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"

	"github.com/knative/serving/pkg/apis/serving/v1alpha1"
	"github.com/knative/test-infra/shared/gcs"
	"github.com/knative/test-infra/shared/testgrid"
)

const (
	logDir         = "logs/ci-knative-serving-continuous"
	buildFile      = "build-log.txt"
	apiCoverage    = "api_coverage"
	overallRoute   = "OverallRoute"
	overallConfig  = "OverallConfiguration"
	overallService = "OverallService"
)

// ResourceObjects defines the resource objects in knative-serving
type ResourceObjects struct {
	Route         *v1alpha1.Route
	Configuration *v1alpha1.Configuration
	Service       *v1alpha1.Service
}

// OverallAPICoverage defines the overall api coverage for knative serving
type OverallAPICoverage struct {
	RouteAPICovered            map[string]int
	RouteAPINotCovered         map[string]int
	ConfigurationAPICovered    map[string]int
	ConfigurationAPINotCovered map[string]int
	ServiceAPICovered          map[string]int
	ServiceAPINotCovered       map[string]int
}

type apiObjectName string

const (
	apiObjectRoute         apiObjectName = "route"
	apiObjectConfiguration               = "configuration"
	apiObjectService                     = "service"
)

// check if the object value is nil or empty.
// Uses https://golang.org/pkg/reflect/#Kind to get the variable type
func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	}
	return false
}

func isStruct(v reflect.Value) bool {
	return v.Kind() == reflect.Struct
}

// Parse the struct and returns a map of <field name, value>
func parseStruct(v reflect.Value) map[string]reflect.Value {
	f := make(map[string]reflect.Value)

	for i := 0; i < v.NumField(); i++ {
		// Include only public vars. https://golang.org/pkg/reflect/#StructField.
		if len(v.Type().Field(i).PkgPath) == 0 {
			f[v.Type().Field(i).Name] = v.Field(i)
		}
	}

	return f
}

func incrementCoverageValues(name string, covered map[string]int) {
	if i, ok := covered[name]; ok {
		covered[name] = i + 1
	} else {
		covered[name] = 1
	}
}

func handleCovered(name string, coverage *OverallAPICoverage) {
	if strings.HasPrefix(name, "route") {
		incrementCoverageValues(name, coverage.RouteAPICovered)
	} else if strings.HasPrefix(name, "configuration") {
		incrementCoverageValues(name, coverage.ConfigurationAPICovered)
	} else if strings.HasPrefix(name, "service") {
		incrementCoverageValues(name, coverage.ServiceAPICovered)
	}
}

func handleNotCovered(name string, coverage *OverallAPICoverage) {
	if strings.HasPrefix(name, "route") {
		coverage.RouteAPINotCovered[name] = 0
	} else if strings.HasPrefix(name, "configuration") {
		coverage.ConfigurationAPINotCovered[name] = 0
	} else if strings.HasPrefix(name, "service") {
		coverage.ServiceAPINotCovered[name] = 0
	}
}

func getCoverage(value reflect.Value, name string, coverage *OverallAPICoverage) {
	// Parse all the fields in the struct
	for key, v := range parseStruct(value) {
		name := name + "." + key
		if isStruct(v) {
			getCoverage(v, name, coverage)
		} else {
			// check if it is empty/nil
			if isNil(v) {
				handleNotCovered(name, coverage)
			} else {
				handleCovered(name, coverage)
			}
		}
	}
}

func calculateCoverage(covLogs []string, coverage *OverallAPICoverage) {
	if len(covLogs) == 0 {
		return
	}

	for _, f := range covLogs {
		var obj ResourceObjects
		if err := json.Unmarshal([]byte(f), &obj); err != nil {
			log.Fatalf("Cannot read resource object: %v", err)
		} else {
			if obj.Route != nil {
				getCoverage(reflect.ValueOf(obj.Route).Elem(), "route", coverage)
			} else if obj.Configuration != nil {
				getCoverage(reflect.ValueOf(obj.Configuration).Elem(), "configuration", coverage)
			} else if obj.Service != nil {
				getCoverage(reflect.ValueOf(obj.Service).Elem(), "service", coverage)
			}
		}
	}
}

func initCoverage() *OverallAPICoverage {
	coverage := OverallAPICoverage{}
	coverage.RouteAPICovered = make(map[string]int)
	coverage.RouteAPINotCovered = make(map[string]int)
	coverage.ConfigurationAPICovered = make(map[string]int)
	coverage.ConfigurationAPINotCovered = make(map[string]int)
	coverage.ServiceAPICovered = make(map[string]int)
	coverage.ServiceAPINotCovered = make(map[string]int)

	return &coverage
}

func getRelevantLogs(fields []string) *string {
	// I0727 16:23:30.055] 2018-10-12T18:18:06.835-0700 info	TestRouteCreation	test/configuration.go:34	resource {<resource_name>: <val>}"}
	if len(fields) == 8 && fields[3] == "info" && fields[6] == "resource" {
		s := strings.Join(fields[7:], " ")
		return &s
	}
	return nil
}

func createCases(tcName string, covered map[string]int, notCovered map[string]int) []testgrid.TestCase {
	var tc []testgrid.TestCase

	var percentCovered = float32(100 * len(covered) / (len(covered) + len(notCovered)))
	tp := []testgrid.TestProperty{testgrid.TestProperty{Name: apiCoverage, Value: percentCovered}}
	tc = append(tc, testgrid.TestCase{Name: tcName, Properties: testgrid.TestProperties{Property: tp}, Fail: false})

	for key, value := range covered {
		tp := []testgrid.TestProperty{testgrid.TestProperty{Name: apiCoverage, Value: float32(value)}}
		tc = append(tc, testgrid.TestCase{Name: tcName + "/" + key, Properties: testgrid.TestProperties{Property: tp}, Fail: false})
	}

	for key, value := range notCovered {
		tp := []testgrid.TestProperty{testgrid.TestProperty{Name: apiCoverage, Value: float32(value)}}
		tc = append(tc, testgrid.TestCase{Name: tcName + "/" + key, Properties: testgrid.TestProperties{Property: tp}, Fail: true})
	}
	return tc
}

func createTestgridXML(coverage *OverallAPICoverage, artifactsDir string) {
	tc := createCases(overallRoute, coverage.RouteAPICovered, coverage.RouteAPINotCovered)
	tc = append(tc, createCases(overallConfig, coverage.ConfigurationAPICovered, coverage.ConfigurationAPINotCovered)...)
	tc = append(tc, createCases(overallService, coverage.ServiceAPICovered, coverage.ServiceAPINotCovered)...)
	ts := testgrid.TestSuite{TestCases: tc}

	if err := testgrid.CreateXMLOutput(ts, artifactsDir); err != nil {
		log.Fatalf("Cannot create the xml output file: %v", err)
	}
}

func main() {

	artifactsDir := flag.String("artifacts-dir", "./artifacts", "Directory to store the generated XML file")
	serviceAccount := flag.String("service-account", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), "JSON key file for service account to use")
	flag.Parse()

	// Read the latest-build.txt file to get the latest build number
	ctx := context.Background()
	num, err := gcs.GetLatestBuildNumber(ctx, logDir, *serviceAccount)
	if err != nil {
		log.Fatalf("Cannot get latest build number: %v", err)
	}

	// Calculate coverage
	coverage := initCoverage()
	calculateCoverage(
		gcs.ParseLog(ctx, fmt.Sprintf("%s/%d/%s", logDir, num, buildFile), getRelevantLogs),
		coverage)

	// Write the testgrid xml to artifacts
	createTestgridXML(coverage, *artifactsDir)
}
