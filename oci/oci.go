package oci

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oracle/oci-go-sdk/common"
	"github.com/oracle/oci-go-sdk/common/auth"
	"github.com/oracle/oci-go-sdk/core"
	"github.com/oracle/oci-go-sdk/identity"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/prometheus/prometheus/util/strutil"
)

const (
	ociLabel           = model.MetaLabelPrefix + "oci_"
	ociInstanceID      = ociLabel + "instance_id"
	ociDisplayName     = ociLabel + "display_name"
	ociCompartmentID   = ociLabel + "compartment_id"
	ociCompartmentName = ociLabel + "compartment_name"
	ociTagLabel        = ociLabel + "tag_"
)

var (
	ociSDRefreshFailuresCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "prometheus_sd_oci_refresh_failures_total",
			Help: "The number of OCI-SD refresh failures.",
		})
	ociSDRefreshDuration = prometheus.NewSummary(
		prometheus.SummaryOpts{
			Name: "prometheus_sd_oci_refresh_duration",
			Help: "The duration of a OCI-SD refresh in seconds.",
		})
	// DefaultSDConfig is the default OCI SD configuration.
	DefaultSDConfig = SDConfig{
		Port:                  80,
		RefreshInterval:       model.Duration(60 * time.Second),
		UseInstancePrincipals: true,
	}
)

func init() {
	prometheus.MustRegister(ociSDRefreshFailuresCount)
	prometheus.MustRegister(ociSDRefreshDuration)
}

// SDConfig is the configuration for OCI based service discovery.
type SDConfig struct {
	CompartmentID         string         `yaml:"compartment_id"`
	RootCompartmentID     string         `yaml:"root_compartment_id"`
	DisplayName           string         `yaml:"display_name"`
	RefreshInterval       model.Duration `yaml:"refresh_interval,omitempty"`
	Port                  int            `yaml:"port"`
	UseInstancePrincipals bool           `yaml:"use_instance_principals,omitempty"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *SDConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*c = DefaultSDConfig
	type plain SDConfig
	err := unmarshal((*plain)(c))
	if err != nil {
		return err
	}
	if c.RootCompartmentID == "" && c.CompartmentID == "" || c.RootCompartmentID != "" && c.CompartmentID != "" {
		return fmt.Errorf("OCI SD configuration requires either a specific compartment id or the root compartment id (not both)")
	}
	return nil
}

// Discovery periodically performs OCI-SD requests. It implements
// the Discoverer interface.
type Discovery struct {
	compartmentID     string
	rootCompartmentID string
	displayName       string
	interval          time.Duration
	port              int
	logger            log.Logger
	ociClientWrapper  ociClientWrapper
}

type ociClientWrapper interface {
	// GetCompartmentIDs returns a slice of compartment ids for a given root compartment. Will return an empty slice if the given compartment id does not belong to a root compartment.
	GetCompartmentIDs(ctx context.Context, rootCompartmentID *string) ([]*string, error)
	// GetCompartmentName returns the name of the given compartment
	GetCompartmentName(ctx context.Context, compartmentID *string) (string, error)
	// ListInstances returns a slice of instance structs for instances matching compartmentID and displayName
	ListInstances(ctx context.Context, compartmentID *string, displayName *string) (*instanceResponse, error)
}

type remoteOciClientWrapper struct {
	ociIdentityClient       *identity.IdentityClient
	ociComputeClient        *core.ComputeClient
	ociVirtualNetworkClient *core.VirtualNetworkClient
}

func (o remoteOciClientWrapper) GetCompartmentIDs(ctx context.Context, rootCompartmentID *string) ([]*string, error) {
	listCompartmentsRequest := identity.ListCompartmentsRequest{
		CompartmentId: rootCompartmentID,
	}
	listCompartmentsResponse, err := o.ociIdentityClient.ListCompartments(ctx, listCompartmentsRequest)
	if err != nil {
		return nil, err
	}
	compartmentIDs := []*string{}
	for _, compartmentItem := range listCompartmentsResponse.Items {
		compartmentIDs = append(compartmentIDs, compartmentItem.Id)
	}
	return compartmentIDs, nil
}

func (o remoteOciClientWrapper) GetCompartmentName(ctx context.Context, compartmentID *string) (string, error) {
	getCompartmentRequest := identity.GetCompartmentRequest{
		CompartmentId: compartmentID,
	}
	getCompartmentResponse, err := o.ociIdentityClient.GetCompartment(ctx, getCompartmentRequest)
	if err != nil {
		return "", err
	}
	return *getCompartmentResponse.Name, nil
}

func (o remoteOciClientWrapper) ListInstances(ctx context.Context, compartmentID *string, displayName *string) (*instanceResponse, error) {
	listInstancesRequest := core.ListInstancesRequest{
		CompartmentId:  compartmentID,
		LifecycleState: core.InstanceLifecycleStateRunning,
	}
	if displayName != nil {
		listInstancesRequest.DisplayName = displayName
	}

	listInstancesResponse, err := o.ociComputeClient.ListInstances(ctx, listInstancesRequest)
	if err != nil {
		return nil, err
	}
	instances := []instance{}
	for _, instanceItem := range listInstancesResponse.Items {
		vnicRequest := core.ListVnicAttachmentsRequest{
			InstanceId:    instanceItem.Id,
			CompartmentId: compartmentID,
		}
		vnics, err := o.ociComputeClient.ListVnicAttachments(ctx, vnicRequest)
		if err != nil {
			return nil, fmt.Errorf("error retrieving vnic attachments from OCI: %s", err)
		}
		var privateIP string
		for _, vnicAttachmentItem := range vnics.Items {
			vnicRequest := core.GetVnicRequest{
				VnicId: vnicAttachmentItem.VnicId,
			}
			vnic, err := o.ociVirtualNetworkClient.GetVnic(ctx, vnicRequest)
			if err != nil {
				return nil, fmt.Errorf("error retrieving vnic from OCI: %s", err)
			}
			if vnic.PrivateIp != nil {
				privateIP = *vnic.PrivateIp
			}
		}
		instance := instance{
			ID:            *instanceItem.Id,
			privateIP:     privateIP,
			DisplayName:   *instanceItem.DisplayName,
			CompartmentID: *instanceItem.CompartmentId,
			FreeformTags:  instanceItem.FreeformTags,
		}
		instances = append(instances, instance)
	}
	instanceResponse := &instanceResponse{
		instances: instances,
	}
	return instanceResponse, nil
}

// NewDiscovery returns a new Discovery which periodically refreshes its targets.
func NewDiscovery(conf SDConfig, logger log.Logger) (*Discovery, error) {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	var err error

	var config common.ConfigurationProvider
	if conf.UseInstancePrincipals {
		config, err = auth.InstancePrincipalConfigurationProvider()
		if err != nil {
			return nil, fmt.Errorf("error connecting to api using instance principals: %s", err)
		}
	} else {
		config = common.DefaultConfigProvider()
	}

	computeClient, err := core.NewComputeClientWithConfigurationProvider(config)
	if err != nil {
		return nil, fmt.Errorf("error setting up compute client for OCI: %s", err)
	}

	identityClient, err := identity.NewIdentityClientWithConfigurationProvider(config)
	if err != nil {
		return nil, fmt.Errorf("error setting up vnic client for OCI: %s", err)
	}

	virtualNetworkClient, err := core.NewVirtualNetworkClientWithConfigurationProvider(config)
	if err != nil {
		return nil, fmt.Errorf("error setting up vnic client for OCI: %s", err)
	}

	remoteOciClientWrapper := remoteOciClientWrapper{
		ociComputeClient:        &computeClient,
		ociIdentityClient:       &identityClient,
		ociVirtualNetworkClient: &virtualNetworkClient,
	}

	ociDiscovery := &Discovery{
		compartmentID:     conf.CompartmentID,
		rootCompartmentID: conf.RootCompartmentID,
		displayName:       conf.DisplayName,
		interval:          time.Duration(conf.RefreshInterval),
		port:              conf.Port,
		logger:            logger,
		ociClientWrapper:  remoteOciClientWrapper,
	}
	return ociDiscovery, nil
}

// Run implements the Discoverer interface.
func (d *Discovery) Run(ctx context.Context, ch chan<- []*targetgroup.Group) {
	tgs, err := d.refresh()
	if err != nil {
		level.Error(d.logger).Log("msg", "Refresh failed", "err", err)
	} else {
		select {
		case ch <- tgs:
		case <-ctx.Done():
		}
	}

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tgs, err := d.refresh()
			if err != nil {
				level.Error(d.logger).Log("msg", "Refresh failed", "err", err)
				continue
			}
			select {
			case ch <- tgs:
			case <-ctx.Done():
			}
		case <-ctx.Done():
			return
		}
	}
}

// instanceResponse wraps an oci ListInstancesResponse, i.e. pagination and a list of instances
type instanceResponse struct {
	Page        *string
	OpcNextPage *string
	instances   []instance
}

// instance wraps the relevant attributes for instances, i.e. the data we want to export as labels
type instance struct {
	ID            string
	privateIP     string
	DisplayName   string
	CompartmentID string
	FreeformTags  map[string]string
}

func (d *Discovery) refresh() (tgs []*targetgroup.Group, err error) {
	t0 := time.Now()
	defer func() {
		ociSDRefreshDuration.Observe(time.Since(t0).Seconds())
		if err != nil {
			ociSDRefreshFailuresCount.Inc()
		}
	}()

	ctx := context.Background()

	var compartmentIDs []*string
	if d.rootCompartmentID != "" {
		compartmentIDs, err = d.ociClientWrapper.GetCompartmentIDs(ctx, &d.rootCompartmentID)
		if err != nil {
			return nil, fmt.Errorf("error retrieving compartment ids from OCI: %s", err)
		}
	} else {
		compartmentIDs = []*string{&d.compartmentID}
	}

	var filterDisplayName *string
	if d.displayName == "" {
		filterDisplayName = nil
	} else {
		filterDisplayName = &d.displayName
	}

	for _, compartmentID := range compartmentIDs {
		compartmentName, err := d.ociClientWrapper.GetCompartmentName(ctx, compartmentID)
		if err != nil {
			return nil, fmt.Errorf("error retrieving compartment from OCI: %s", err)
		}

		listInstancesFunc := func(compartmentID *string, displayName *string) (*instanceResponse, error) {
			return d.ociClientWrapper.ListInstances(ctx, compartmentID, displayName)
		}

		for instanceResponse, err := listInstancesFunc(compartmentID, filterDisplayName); ; instanceResponse, err = listInstancesFunc(compartmentID, filterDisplayName) {
			if err != nil {
				return tgs, fmt.Errorf("error retrieving targets from oci: %s", err)
			}
			for _, instance := range instanceResponse.instances {
				privateIP := instance.privateIP
				addr := fmt.Sprintf("%s:%d", privateIP, d.port)
				target := model.LabelSet{
					model.AddressLabel: model.LabelValue(addr),
				}
				labels := model.LabelSet{
					ociInstanceID:      model.LabelValue(instance.ID),
					ociDisplayName:     model.LabelValue(instance.DisplayName),
					ociCompartmentID:   model.LabelValue(instance.CompartmentID),
					ociCompartmentName: model.LabelValue(compartmentName),
					model.AddressLabel: model.LabelValue(addr),
				}
				for key, value := range instance.FreeformTags {
					name := strutil.SanitizeLabelName(key)
					labels[ociTagLabel+model.LabelName(name)] = model.LabelValue(value)
				}
				tg := &targetgroup.Group{
					Source:  fmt.Sprintf("OCI_%s_", instance.ID),
					Labels:  labels,
					Targets: []model.LabelSet{target},
				}
				tgs = append(tgs, tg)
			}

			if instanceResponse.OpcNextPage != nil {
				instanceResponse.Page = instanceResponse.OpcNextPage
			} else {
				break
			}
		}
	}
	return tgs, nil
}
