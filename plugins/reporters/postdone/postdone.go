// Copyright (c) Facebook, Inc. and its affiliates.
//
// This source code is licensed under the MIT license found in the
// LICENSE file in the root directory of this source tree.

package postdone

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/facebookincubator/contest/pkg/event/testevent"
	"github.com/facebookincubator/contest/pkg/job"
	"github.com/facebookincubator/contest/pkg/logging"
)

// Name defines the name of the reporter used within the plugin registry
var Name = "postdone"

var log = logging.GetLogger("reporter/" + strings.ToLower(Name))

// postdone is a reporter that does nothing. Probably only useful for testing.
type postdone struct{}

type FinalParameters struct {
	ApiURI string
}

// ValidateRunParameters validates the parameters for the run reporter
func (d *postdone) ValidateRunParameters(params []byte) (interface{}, error) {
	var s string
	return s, nil
}

// ValidateFinalParameters validates the parameters for the final reporter
func (d *postdone) ValidateFinalParameters(params []byte) (interface{}, error) {
	var fp FinalParameters
	if err := json.Unmarshal(params, &fp); err != nil {
		return nil, err
	}
	_, err := url.ParseRequestURI(fp.ApiURI)
	if err != nil {
		log.Errorf("ApiURI is not formatted right")
		return fp, err
	}
	return fp, nil
}

// Name returns the Name of the reporter
func (d *postdone) Name() string {
	return Name
}

// RunReport calculates the report to be associated with a job run.
func (d *postdone) RunReport(ctx context.Context, parameters interface{}, runStatus *job.RunStatus, ev testevent.Fetcher) (bool, interface{}, error) {
	return true, "I did nothing", nil
}

// FinalReport calculates the final report to be associated to a job.
func (d *postdone) FinalReport(ctx context.Context, parameters interface{}, runStatuses []job.RunStatus, ev testevent.Fetcher) (bool, interface{}, error) {
	fp := parameters.(FinalParameters)
	data := map[string]string{
		"status": "iamdone",
	}
	json_data, err := json.Marshal(data)
	if err != nil {
		log.Errorf("Could not parse data to json format.")
	}
	resp, err := http.Post(fp.ApiURI, "application/json", bytes.NewBuffer(json_data))
	if err != nil {
		log.Errorf("Could not post data to API.")
		return false, "", nil
	}
	switch statuscode := resp.StatusCode; statuscode {
	case 200:
		log.Infof("HTTP Post was successfull: OK")
	case 400:
		log.Errorf("HTTP Post was not successfull: Bad Request")
	case 401:
		log.Errorf("HTTP Post was not successfull: Unauthorized")
	case 405:
		log.Errorf("HTTP Post was not successfull: Method Not Allowed")
	case 500:
		log.Errorf("HTTP Post was not successfull: Internal Server Error")
	default:
		log.Errorf("HTTP Post was not successfull with statuscode: %v \n", statuscode)
	}
	return true, "", nil
}

// New builds a new TargetSuccessReporter
func New() job.Reporter {
	return &postdone{}
}

// Load returns the name and factory which are needed to register the Reporter
func Load() (string, job.ReporterFactory) {
	return Name, New
}
