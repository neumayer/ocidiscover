package oci

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/prometheus/prometheus/util/testutil"
)

var testCompartmentName = "compartment_name1"
var testCompartmentID = "compartment_id1"
var testInstanceID = "instance_id1"
var testInstanceDisplayName = "instance_name1"
var testInstancePrivateIP = "127.0.0.1"
var testInstancePort = 9100

var target = model.LabelSet{
	model.AddressLabel: model.LabelValue(fmt.Sprintf("%s:%d", testInstancePrivateIP, testInstancePort)),
}

var labels = model.LabelSet{
	ociInstanceID:      model.LabelValue(testInstanceID),
	ociDisplayName:     model.LabelValue(testInstanceDisplayName),
	ociCompartmentID:   model.LabelValue(testCompartmentID),
	ociCompartmentName: model.LabelValue(testCompartmentName),
	model.AddressLabel: model.LabelValue(fmt.Sprintf("%s:%d", testInstancePrivateIP, testInstancePort)),
}

var expectedTargetGroup = &targetgroup.Group{
	Source:  fmt.Sprintf("OCI_%s_", testInstanceID),
	Targets: []model.LabelSet{target},
	Labels:  labels,
}

type testOciClientWrapper struct {
}

func (f testOciClientWrapper) GetCompartmentIDs(ctx context.Context, rootCompartmentID *string) ([]*string, error) {
	id := testCompartmentID
	ids := []*string{&id}
	return ids, nil
}

func (f testOciClientWrapper) GetCompartmentName(ctx context.Context, compartmentID *string) (string, error) {
	return testCompartmentName, nil
}

func (f testOciClientWrapper) ListInstances(ctx context.Context, compartmentID *string, displayName *string) (*instanceResponse, error) {
	if displayName != nil && testInstanceDisplayName != *displayName {
		return &instanceResponse{nil, nil, []instance{}}, nil
	}
	instances := []instance{
		instance{
			ID:            testInstanceID,
			DisplayName:   testInstanceDisplayName,
			CompartmentID: testCompartmentID,
			privateIP:     testInstancePrivateIP,
		},
	}
	instanceResponse := &instanceResponse{
		instances: instances,
	}
	return instanceResponse, nil
}

func TestRefresh(t *testing.T) {
	clientWrapper := &testOciClientWrapper{}
	discovery := Discovery{
		compartmentID:    testCompartmentID,
		port:             testInstancePort,
		ociClientWrapper: clientWrapper,
	}
	tgs, _ := discovery.refresh()
	checkTarget(t, tgs)
}

func TestRun(t *testing.T) {
	clientWrapper := &testOciClientWrapper{}
	discovery := Discovery{
		compartmentID:    testCompartmentID,
		interval:         time.Duration(60 * time.Second),
		port:             testInstancePort,
		ociClientWrapper: clientWrapper,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan []*targetgroup.Group)
	go discovery.Run(ctx, ch)
	checkTarget(t, <-ch)
	cancel()
}

func TestRunFilterDisplayName(t *testing.T) {
	clientWrapper := &testOciClientWrapper{}
	discovery := Discovery{
		compartmentID:    testCompartmentID,
		displayName:      testInstanceDisplayName,
		interval:         time.Duration(60 * time.Second),
		port:             testInstancePort,
		ociClientWrapper: clientWrapper,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan []*targetgroup.Group)
	go discovery.Run(ctx, ch)
	checkTarget(t, <-ch)
	cancel()
}

func checkTarget(t *testing.T, targetGroups []*targetgroup.Group) {
	testutil.Equals(t, 1, len(targetGroups))
	target := targetGroups[0]
	if target.Source != expectedTargetGroup.Source {
		t.Errorf("target groups sources do not match returned: %v, expected %v", target, expectedTargetGroup)
	}
	if !reflect.DeepEqual(target.Targets, expectedTargetGroup.Targets) {
		t.Errorf("target groups do not match, returned: %v, expected %v", target.Targets, expectedTargetGroup.Targets)
	}
}
