// Copyright (c) Facebook, Inc. and its affiliates.
//
// This source code is licensed under the MIT license found in the
// LICENSE file in the root directory of this source tree.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/facebookincubator/contest/pkg/abstract"
	"github.com/facebookincubator/contest/pkg/config"
	"github.com/facebookincubator/contest/pkg/job"
	"github.com/facebookincubator/contest/pkg/jobmanager"
	"github.com/facebookincubator/contest/pkg/logging"
	"github.com/facebookincubator/contest/pkg/pluginregistry"
	"github.com/facebookincubator/contest/pkg/storage"
	"github.com/facebookincubator/contest/pkg/target"
	"github.com/facebookincubator/contest/pkg/test"
	"github.com/facebookincubator/contest/plugins/listeners/httplistener"
	reportersNoop "github.com/facebookincubator/contest/plugins/reporters/noop"
	"github.com/facebookincubator/contest/plugins/reporters/targetsuccess"
	"github.com/facebookincubator/contest/plugins/storage/rdbms"
	"github.com/facebookincubator/contest/plugins/targetlocker/inmemory"
	"github.com/facebookincubator/contest/plugins/targetlocker/mysql"
	targetLockerNoop "github.com/facebookincubator/contest/plugins/targetlocker/noop"
	"github.com/facebookincubator/contest/plugins/targetmanagers/csvtargetmanager"
	"github.com/facebookincubator/contest/plugins/targetmanagers/targetlist"
	"github.com/facebookincubator/contest/plugins/testfetchers/literal"
	"github.com/facebookincubator/contest/plugins/testfetchers/uri"
	"github.com/facebookincubator/contest/plugins/teststeps/cmd"
	"github.com/facebookincubator/contest/plugins/teststeps/echo"
	"github.com/facebookincubator/contest/plugins/teststeps/example"
	"github.com/facebookincubator/contest/plugins/teststeps/randecho"
	"github.com/facebookincubator/contest/plugins/teststeps/slowecho"
	"github.com/facebookincubator/contest/plugins/teststeps/sshcmd"
	"github.com/facebookincubator/contest/plugins/teststeps/terminalexpect"
	"github.com/sirupsen/logrus"
)

const (
	defaultDBURI        = "contest:contest@tcp(localhost:3306)/contest?parseTime=true"
	defaultTargetLocker = "MySQL:%dbURI%"
)

var (
	flagDBURI, flagTargetLocker *string
)

func setupFlags() {
	var targetLockerPluginNames []string
	for _, factory := range targetLockerFactories {
		targetLockerPluginNames = append(targetLockerPluginNames, factory.UniqueImplementationName())
	}

	flagDBURI = flag.String("dbURI", defaultDBURI, "MySQL DSN")
	flagTargetLocker = flag.String("targetLocker", defaultTargetLocker,
		fmt.Sprintf("The engine to lock targets. Possible engines (the part before the first colon): %s",
			strings.Join(targetLockerPluginNames, ", "),
		))
	flag.Parse()
}

var log = logging.GetLogger("contest")

var targetManagerFactories = []target.TargetManagerFactory{
	&csvtargetmanager.Factory{},
	&targetlist.Factory{},
}

var targetLockerFactories = []target.LockerFactory{
	&mysql.Factory{},
	&inmemory.Factory{},
	&targetLockerNoop.Factory{},
}

var testFetcherFactories = []test.TestFetcherFactory{
	&uri.Factory{},
	&literal.Factory{},
}

var testStepFactories = []test.TestStepFactory{
	&echo.Factory{},
	&slowecho.Factory{},
	&example.Factory{},
	&cmd.Factory{},
	&sshcmd.Factory{},
	&randecho.Factory{},
	&terminalexpect.Factory{},
}

var reporterFactories = []job.ReporterFactory{
	&targetsuccess.Factory{},
	&reportersNoop.Factory{},
}

// user-defined functions that will be made available to plugins for advanced
// expressions in config parameters.
var userFunctions = map[string]interface{}{
	// dummy function to prove that function registration works.
	"do_nothing": func(a ...string) (string, error) {
		if len(a) == 0 {
			return "", errors.New("do_nothing: no arg specified")
		}
		return a[0], nil
	},
}

// expandArgument expands macros like '%dbURI%' to values using flag.CommandLine
func expandArgument(arg string) string {
	// it does not support correct expanding into depth more one.
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		arg = strings.Replace(arg, `%`+f.Name+`%`, f.Value.String(), -1)
	})
	return arg
}

func parseFactoryInfo(
	factoryType pluginregistry.FactoryType,
	flagValue string,
) (factory abstract.Factory, factoryImplName, factoryArgument string) {

	factoryInfo := strings.SplitN(flagValue, `:`, 2)
	factoryImplName = factoryInfo[0]

	if len(factoryInfo) > 1 {
		factoryArgument = expandArgument(factoryInfo[1])
	}

	var err error
	factory, err = pluginRegistry.Factory(factoryType, factoryImplName)
	if factory == nil || err != nil {
		factories, _ := pluginRegistry.Factories(factoryType)
		var knownPluginNames []string
		for _, factory := range factories {
			knownPluginNames = append(knownPluginNames, factory.UniqueImplementationName())
		}
		log.Fatalf("Implementation '%s' is not found (possible values: %s)",
			factoryImplName, strings.Join(knownPluginNames, ", "))
	}
	return
}

var pluginRegistry *pluginregistry.PluginRegistry

// setupPluginRegistry initializes pluginRegistry
func setupPluginRegistry() error {

	pluginRegistry = pluginregistry.NewPluginRegistry()

	for _, factory := range targetManagerFactories {
		if err := pluginRegistry.RegisterFactory(factory); err != nil {
			return fmt.Errorf("unable to register target manager factory %T: %w", factory, err)
		}
	}

	for _, factory := range targetLockerFactories {
		if err := pluginRegistry.RegisterFactory(factory); err != nil {
			return fmt.Errorf("unable to register target locker factory %T: %w", factory, err)
		}
	}

	for _, factory := range testStepFactories {
		if err := pluginRegistry.RegisterFactory(factory); err != nil {
			return fmt.Errorf("unable to register test step factory %T: %w", factory, err)
		}
	}

	for _, factory := range testFetcherFactories {
		if err := pluginRegistry.RegisterFactory(factory); err != nil {
			return fmt.Errorf("unable to register test fetcher factory %T: %w", factory, err)
		}
	}

	for _, factory := range reporterFactories {
		if err := pluginRegistry.RegisterFactory(factory); err != nil {
			return fmt.Errorf("unable to register job reporter factory %T: %w", factory, err)
		}
	}

	return nil
}

func main() {
	setupFlags()

	logrus.SetLevel(logrus.DebugLevel)
	log.Level = logrus.DebugLevel

	err := setupPluginRegistry()
	if err != nil {
		log.Fatal(err)
	}

	// storage initialization
	log.Infof("Using database URI (MySQL DSN) for the main storage: %s", *flagDBURI)
	storage.SetStorage(rdbms.New(*flagDBURI))

	// set Locker engine
	targetLockerFactory, targetLockerImplName, targetLockerArgument :=
		parseFactoryInfo(pluginregistry.FactoryTypeTargetLocker, *flagTargetLocker)

	log.Infof("Using target locker '%s' with argument: '%s'", targetLockerImplName, targetLockerArgument)
	targetLocker, err := targetLockerFactory.(target.LockerFactory).New(config.LockTimeout, targetLockerArgument)
	if err != nil {
		log.Fatalf("Unable to initialize target locker: %v", err)
	}
	target.SetLocker(targetLocker)

	// user-defined function registration
	for name, fn := range userFunctions {
		if err := test.RegisterFunction(name, fn); err != nil {
			log.Fatal(err)
		}
	}

	// spawn JobManager
	listener := httplistener.HTTPListener{}

	jm, err := jobmanager.New(&listener, pluginRegistry)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("JobManager %+v", jm)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	if err := jm.Start(sigs); err != nil {
		log.Fatal(err)
	}
}
