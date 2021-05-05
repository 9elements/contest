package clientpluginregistry

import (
	"sync"

	"github.com/facebookincubator/contest/pkg/xcontext"
)

type ClientPluginRegistry struct {
	lock sync.RWMutex

	Context xcontext.Context

	// PreJobExecutionHooks are hooks which gets executed before posting the job to the server
	PreJobExecutionHooks map[string]client.PreJobExecutionHooksFactory

	// PostJobExecutionHooks are hooks which gets executed after the job has been processed(!) by the server
	PostJobExecutionHooks map[string]client.PostJobExecutionHooksFactory
}
