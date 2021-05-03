// Copyright (c) Facebook, Inc. and its affiliates.
//
// This source code is licensed under the MIT license found in the
// LICENSE file in the root directory of this source tree.

// Package csvtargetmanager implements a simple target manager that parses a CSV
// file. The format of the CSV file is the following:
//
// 123,hostname1.example.com,1.2.3.4,
// 456,hostname2,,2001:db8::1
//
// In other words, four fields: the first containing a unique ID for the device
// (might be identical to the IP or FQDN), next one is FQDN,
// and then IPv4 and IPv6.
// All fields except ID are optional, but many plugins require FQDN or IP fields to
// reach the targets over the network.
package mysqltargetmanager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/facebookincubator/contest/pkg/config"
	"github.com/facebookincubator/contest/pkg/logging"
	"github.com/facebookincubator/contest/pkg/target"
	"github.com/facebookincubator/contest/pkg/types"

	// this blank import registers the mysql driver
	_ "github.com/go-sql-driver/mysql"
)

// Name defined the name of the plugin
var (
	Name = "MySQLTargetManager"
)

var log = logging.GetLogger("targetmanagers/" + strings.ToLower(Name))

// AcquireParameters contains the parameters necessary to acquire targets.
type AcquireParameters struct {
	MinNumberDevices uint32
	MaxNumberDevices uint32
	HostPrefixes     []string
	Shuffle          bool
}

type mysqltable struct {
	ID     uint16
	hostid string
	FQDN   string
	IPv4   string
	IPv6   string
}

// ReleaseParameters contains the parameters necessary to release targets.
type ReleaseParameters struct {
}

// MySQLTargetManager implements the contest.TargetManager interface, reading
// CSV entries from a text file.
type MySQLTargetManager struct {
	hosts []*target.Target
}

// ValidateAcquireParameters performs sanity checks on the fields of the
// parameters that will be passed to Acquire.
func (tf MySQLTargetManager) ValidateAcquireParameters(params []byte) (interface{}, error) {
	var ap AcquireParameters
	if err := json.Unmarshal(params, &ap); err != nil {
		return nil, err
	}
	for idx, hp := range ap.HostPrefixes {
		hp = strings.TrimSpace(hp)
		if hp == "" {
			return nil, fmt.Errorf("host prefix cannot be empty string if specified")
		}
		// reassign after removing surrounding spaces
		ap.HostPrefixes[idx] = hp
	}
	return ap, nil
}

// ValidateReleaseParameters performs sanity checks on the fields of the
// parameters that will be passed to Release.
func (tf MySQLTargetManager) ValidateReleaseParameters(params []byte) (interface{}, error) {
	var rp ReleaseParameters
	if err := json.Unmarshal(params, &rp); err != nil {
		return nil, err
	}
	return rp, nil
}

// Acquire implements contest.TargetManager.Acquire, reading one entry per line
// from a text file. Each input record looks like this: ID,FQDN,IPv4,IPv6. Only ID is required
func (tf *MySQLTargetManager) Acquire(ctx context.Context, jobID types.JobID, jobTargetManagerAcquireTimeout time.Duration, parameters interface{}, tl target.Locker) ([]*target.Target, error) {
	acquireParameters, ok := parameters.(AcquireParameters)
	if !ok {
		return nil, fmt.Errorf("Acquire expects %T object, got %T", acquireParameters, parameters)
	}
	db, err := sql.Open("mysql", config.DefaultDBURI)
	if err != nil {
		log.Errorf("Could not open the mysql DB. ", err)
	}
	defer db.Close()

	hosts := make([]*target.Target, 0)

	table, err := db.Query("SELECT id, hostid, FQDN, IPv4, IPv6 FROM targetmanager")
	if err != nil {
		log.Errorf("Could not query data from table. ", err)
	}
	var data mysqltable

	for table.Next() {
		if err := table.Scan(&data.ID, &data.hostid, &data.FQDN, &data.IPv4, &data.IPv6); err != nil {
			return nil, errors.New("scan column from table waws not successful")
		}
		if data.hostid == "" && data.FQDN == "" && data.IPv4 == "" && data.IPv6 == "" {
			log.Errorf("Column is not filled with valid data.")
			continue
		}
		if data.hostid == "" {
			return nil, errors.New("invalid empty string for host ID")
		}
		var t target.Target
		t.ID = data.hostid
		if data.FQDN != "" {
			// no validation on fqdns
			t.FQDN = data.FQDN
		}
		if data.IPv4 != "" {
			t.PrimaryIPv4 = net.ParseIP(data.IPv4)
			if t.PrimaryIPv4 == nil || t.PrimaryIPv4.To4() == nil {
				// didn't parse
				return nil, fmt.Errorf("invalid non-empty IPv4 address \"%s\"", data.IPv4)
			}
		}
		if data.IPv6 != "" { //optimieren
			t.PrimaryIPv6 = net.ParseIP(data.IPv6)
			if t.PrimaryIPv6 == nil || t.PrimaryIPv6.To16() == nil {
				// didn't parse
				return nil, fmt.Errorf("invalid non-empty IPv6 address \"%s\"", data.IPv6)
			}
		}
		if len(acquireParameters.HostPrefixes) == 0 {
			hosts = append(hosts, &t)
		} else if t.FQDN != "" {
			// host prefix filtering only works on devices with a FQDN
			firstComponent := strings.Split(t.FQDN, ".")[0]
			for _, hp := range acquireParameters.HostPrefixes {
				if strings.HasPrefix(firstComponent, hp) {
					hosts = append(hosts, &t)
				}
			}
		}
	}
	if uint32(len(hosts)) < acquireParameters.MinNumberDevices {
		return nil, fmt.Errorf("not enough hosts found in DB table 'targetmanager', want %d, got %d",
			acquireParameters.MinNumberDevices,
			len(hosts),
		)
	}
	log.Printf("Found %d targets in DB table 'targetmanager'", len(hosts))
	if acquireParameters.Shuffle {
		log.Info("Shuffling targets")
		rand.Shuffle(len(hosts), func(i, j int) {
			hosts[i], hosts[j] = hosts[j], hosts[i]
		})
	}

	// feed all devices into new API call `TryLock`, with desired limit
	lockedString, err := tl.TryLock(jobID, jobTargetManagerAcquireTimeout, hosts, uint(acquireParameters.MaxNumberDevices))
	if err != nil {
		return nil, fmt.Errorf("failed to lock targets: %w", err)
	}
	locked, err := target.FilterTargets(lockedString, hosts)
	if err != nil {
		return nil, fmt.Errorf("can not find locked targets in hosts")
	}

	// check if we got enough
	if len(locked) >= int(acquireParameters.MinNumberDevices) {
		// done, we got enough and they are locked
	} else {
		// not enough, unlock what we got and fail
		if len(locked) > 0 {
			err = tl.Unlock(jobID, locked)
			if err != nil {
				return nil, fmt.Errorf("can't unlock targets")
			}
		}
		return nil, fmt.Errorf("can't lock enough targets")
	}

	tf.hosts = locked
	return locked, nil
}

// Release releases the acquired resources.
func (tf *MySQLTargetManager) Release(ctx context.Context, jobID types.JobID, params interface{}) error {
	return nil
}

// New builds a MySQLTargetManager
func New() target.TargetManager {
	return &MySQLTargetManager{}
}

// Load returns the name and factory which are needed to register the
// TargetManager.
func Load() (string, target.TargetManagerFactory) {
	return Name, New
}
