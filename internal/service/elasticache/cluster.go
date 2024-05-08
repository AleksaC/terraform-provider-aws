// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package elasticache

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YakDriver/regexache"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elasticache"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/errs"
	"github.com/hashicorp/terraform-provider-aws/internal/errs/sdkdiag"
	"github.com/hashicorp/terraform-provider-aws/internal/flex"
	"github.com/hashicorp/terraform-provider-aws/internal/sdkv2/types/nullable"
	tfslices "github.com/hashicorp/terraform-provider-aws/internal/slices"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
	"github.com/hashicorp/terraform-provider-aws/names"
)

const (
	defaultRedisPort     = "6379"
	defaultMemcachedPort = "11211"
)

// @SDKResource("aws_elasticache_cluster", name="Cluster")
// @Tags(identifierAttribute="arn")
func resourceCluster() *schema.Resource {
	return &schema.Resource{
		CreateWithoutTimeout: resourceClusterCreate,
		ReadWithoutTimeout:   resourceClusterRead,
		UpdateWithoutTimeout: resourceClusterUpdate,
		DeleteWithoutTimeout: resourceClusterDelete,

		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"apply_immediately": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
			},
			names.AttrARN: {
				Type:     schema.TypeString,
				Computed: true,
			},
			"auto_minor_version_upgrade": {
				Type:         nullable.TypeNullableBool,
				Optional:     true,
				Default:      "true",
				ValidateFunc: nullable.ValidateTypeStringNullableBool,
			},
			names.AttrAvailabilityZone: {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"az_mode": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringInSlice(elasticache.AZMode_Values(), false),
			},
			"cache_nodes": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"address": {
							Type:     schema.TypeString,
							Computed: true,
						},
						names.AttrAvailabilityZone: {
							Type:     schema.TypeString,
							Computed: true,
						},
						names.AttrID: {
							Type:     schema.TypeString,
							Computed: true,
						},
						"outpost_arn": {
							Type:     schema.TypeString,
							Computed: true,
						},
						names.AttrPort: {
							Type:     schema.TypeInt,
							Computed: true,
						},
					},
				},
			},
			"cluster_address": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"cluster_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				StateFunc: func(val interface{}) string {
					// ElastiCache normalizes cluster ids to lowercase,
					// so we have to do this too or else we can end up
					// with non-converging diffs.
					return strings.ToLower(val.(string))
				},
				ValidateFunc: validation.All(
					validation.StringLenBetween(1, 50),
					validation.StringMatch(regexache.MustCompile(`^[0-9a-z-]+$`), "must contain only lowercase alphanumeric characters and hyphens"),
					validation.StringMatch(regexache.MustCompile(`^[a-z]`), "must begin with a lowercase letter"),
					validation.StringDoesNotMatch(regexache.MustCompile(`--`), "cannot contain two consecutive hyphens"),
					validation.StringDoesNotMatch(regexache.MustCompile(`-$`), "cannot end with a hyphen"),
				),
			},
			"configuration_endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"engine": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ExactlyOneOf: []string{"engine", "replication_group_id"},
				ValidateFunc: validation.StringInSlice(engine_Values(), false),
			},
			"engine_version": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"engine_version_actual": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"final_snapshot_identifier": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"ip_discovery": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringInSlice(elasticache.IpDiscovery_Values(), false),
			},
			"log_delivery_configuration": {
				Type:     schema.TypeSet,
				MaxItems: 2,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"destination": {
							Type:     schema.TypeString,
							Required: true,
						},
						"destination_type": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringInSlice(elasticache.DestinationType_Values(), false),
						},
						"log_format": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringInSlice(elasticache.LogFormat_Values(), false),
						},
						"log_type": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringInSlice(elasticache.LogType_Values(), false),
						},
					},
				},
			},
			"maintenance_window": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				StateFunc: func(val interface{}) string {
					// ElastiCache always changes the maintenance
					// to lowercase
					return strings.ToLower(val.(string))
				},
				ValidateFunc: verify.ValidOnceAWeekWindowFormat,
			},
			"network_type": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice(elasticache.NetworkType_Values(), false),
			},
			"node_type": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"notification_topic_arn": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"num_cache_nodes": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"outpost_mode": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				RequiredWith: []string{"preferred_outpost_arn"},
				ValidateFunc: validation.StringInSlice(elasticache.OutpostMode_Values(), false),
			},
			"parameter_group_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			names.AttrPort: {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				ForceNew: true,
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					// Suppress default memcached/redis ports when not defined
					if !d.IsNewResource() && new == "0" && (old == defaultRedisPort || old == defaultMemcachedPort) {
						return true
					}
					return false
				},
			},
			"preferred_availability_zones": {
				Type:     schema.TypeList,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"preferred_outpost_arn": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"replication_group_id": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ExactlyOneOf: []string{"replication_group_id", "engine"},
				ValidateFunc: validateReplicationGroupID,
				ConflictsWith: []string{
					"az_mode",
					"engine_version",
					"maintenance_window",
					"node_type",
					"notification_topic_arn",
					"num_cache_nodes",
					"parameter_group_name",
					names.AttrPort,
					names.AttrSecurityGroupIDs,
					"snapshot_arns",
					"snapshot_name",
					"snapshot_retention_limit",
					"snapshot_window",
					"subnet_group_name",
				},
			},
			names.AttrSecurityGroupIDs: {
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"snapshot_arns": {
				Type:     schema.TypeList,
				MaxItems: 1,
				Optional: true,
				ForceNew: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
					ValidateFunc: validation.All(
						verify.ValidARN,
						validation.StringDoesNotContainAny(","),
					),
				},
			},
			"snapshot_name": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},
			"snapshot_retention_limit": {
				Type:         schema.TypeInt,
				Optional:     true,
				ValidateFunc: validation.IntAtMost(35),
			},
			"snapshot_window": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: verify.ValidOnceADayWindowFormat,
			},
			"subnet_group_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"transit_encryption_enabled": {
				Type:     schema.TypeBool,
				ForceNew: true,
				Optional: true,
				Computed: true,
			},
			names.AttrTags:    tftags.TagsSchema(),
			names.AttrTagsAll: tftags.TagsSchemaComputed(),
		},

		CustomizeDiff: customdiff.Sequence(
			customizeDiffValidateClusterAZMode,
			customizeDiffValidateClusterEngineVersion,
			customizeDiffEngineVersionForceNewOnDowngrade,
			customizeDiffValidateClusterNumCacheNodes,
			customizeDiffClusterMemcachedNodeType,
			customizeDiffValidateClusterMemcachedSnapshotIdentifier,
			verify.SetTagsDiff,
		),
	}
}

func resourceClusterCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ElastiCacheConn(ctx)

	clusterID := d.Get("cluster_id").(string)
	input := &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(clusterID),
		Tags:           getTagsIn(ctx),
	}

	if v, ok := d.GetOk("replication_group_id"); ok {
		input.ReplicationGroupId = aws.String(v.(string))
	} else {
		input.SecurityGroupIds = flex.ExpandStringSet(d.Get(names.AttrSecurityGroupIDs).(*schema.Set))
	}

	if v, ok := d.GetOk("node_type"); ok {
		input.CacheNodeType = aws.String(v.(string))
	}

	if v, ok := d.GetOk("num_cache_nodes"); ok {
		input.NumCacheNodes = aws.Int64(int64(v.(int)))
	}

	if v, ok := d.GetOk("outpost_mode"); ok {
		input.OutpostMode = aws.String(v.(string))
	}

	if v, ok := d.GetOk("preferred_outpost_arn"); ok {
		input.PreferredOutpostArn = aws.String(v.(string))
	}

	if v, ok := d.GetOk("engine"); ok {
		input.Engine = aws.String(v.(string))
	}

	version := d.Get("engine_version").(string)
	if version != "" {
		input.EngineVersion = aws.String(version)
	}

	if v, ok := d.GetOk("auto_minor_version_upgrade"); ok {
		if v, null, _ := nullable.Bool(v.(string)).ValueBool(); !null {
			input.AutoMinorVersionUpgrade = aws.Bool(v)
		}
	}

	if v, ok := d.GetOk(names.AttrPort); ok {
		input.Port = aws.Int64(int64(v.(int)))
	}

	if v, ok := d.GetOk("subnet_group_name"); ok {
		input.CacheSubnetGroupName = aws.String(v.(string))
	}

	// parameter groups are optional and can be defaulted by AWS
	if v, ok := d.GetOk("parameter_group_name"); ok {
		input.CacheParameterGroupName = aws.String(v.(string))
	}

	if v, ok := d.GetOk("snapshot_retention_limit"); ok {
		input.SnapshotRetentionLimit = aws.Int64(int64(v.(int)))
	}

	if v, ok := d.GetOk("snapshot_window"); ok {
		input.SnapshotWindow = aws.String(v.(string))
	}

	if v, ok := d.GetOk("log_delivery_configuration"); ok {
		input.LogDeliveryConfigurations = []*elasticache.LogDeliveryConfigurationRequest{}
		v := v.(*schema.Set).List()
		for _, v := range v {
			logDeliveryConfigurationRequest := expandLogDeliveryConfigurations(v.(map[string]interface{}))
			input.LogDeliveryConfigurations = append(input.LogDeliveryConfigurations, &logDeliveryConfigurationRequest)
		}
	}

	if v, ok := d.GetOk("maintenance_window"); ok {
		input.PreferredMaintenanceWindow = aws.String(v.(string))
	}

	if v, ok := d.GetOk("notification_topic_arn"); ok {
		input.NotificationTopicArn = aws.String(v.(string))
	}

	snaps := d.Get("snapshot_arns").([]interface{})
	if len(snaps) > 0 {
		input.SnapshotArns = flex.ExpandStringList(snaps)
		log.Printf("[DEBUG] Restoring Redis cluster from S3 snapshot: %#v", snaps)
	}

	if v, ok := d.GetOk("snapshot_name"); ok {
		input.SnapshotName = aws.String(v.(string))
	}

	if v, ok := d.GetOk("transit_encryption_enabled"); ok {
		input.TransitEncryptionEnabled = aws.Bool(v.(bool))
	}

	if v, ok := d.GetOk("az_mode"); ok {
		input.AZMode = aws.String(v.(string))
	}

	if v, ok := d.GetOk(names.AttrAvailabilityZone); ok {
		input.PreferredAvailabilityZone = aws.String(v.(string))
	}

	if v, ok := d.GetOk("preferred_availability_zones"); ok && len(v.([]interface{})) > 0 {
		input.PreferredAvailabilityZones = flex.ExpandStringList(v.([]interface{}))
	}

	if v, ok := d.GetOk("ip_discovery"); ok {
		input.IpDiscovery = aws.String(v.(string))
	}

	if v, ok := d.GetOk("network_type"); ok {
		input.NetworkType = aws.String(v.(string))
	}

	id, arn, err := createCacheCluster(ctx, conn, input)

	if err != nil {
		return sdkdiag.AppendErrorf(diags, "creating ElastiCache Cache Cluster (%s): %s", clusterID, err)
	}

	d.SetId(id)

	const (
		timeout = 40 * time.Minute
	)
	if _, err := waitCacheClusterAvailable(ctx, conn, d.Id(), timeout); err != nil {
		return sdkdiag.AppendErrorf(diags, "waiting for ElastiCache Cache Cluster (%s) create: %s", d.Id(), err)
	}

	// For partitions not supporting tag-on-create, attempt tag after create.
	if tags := getTagsIn(ctx); input.Tags == nil && len(tags) > 0 {
		err := createTags(ctx, conn, arn, tags)

		// If default tags only, continue. Otherwise, error.
		if v, ok := d.GetOk(names.AttrTags); (!ok || len(v.(map[string]interface{})) == 0) && errs.IsUnsupportedOperationInPartitionError(conn.PartitionID, err) {
			return append(diags, resourceClusterRead(ctx, d, meta)...)
		}

		if err != nil {
			return sdkdiag.AppendErrorf(diags, "setting ElastiCache Cache Cluster (%s) tags: %s", d.Id(), err)
		}
	}

	return append(diags, resourceClusterRead(ctx, d, meta)...)
}

func resourceClusterRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ElastiCacheConn(ctx)

	c, err := findCacheClusterWithNodeInfoByID(ctx, conn, d.Id())

	if !d.IsNewResource() && tfresource.NotFound(err) {
		log.Printf("[WARN] ElastiCache Cache Cluster (%s) not found, removing from state", d.Id())
		d.SetId("")
		return diags
	}

	if err != nil {
		return sdkdiag.AppendErrorf(diags, "reading ElastiCache Cache Cluster (%s): %s", d.Id(), err)
	}

	d.Set("cluster_id", c.CacheClusterId)

	if err := setFromCacheCluster(d, c); err != nil {
		return sdkdiag.AppendErrorf(diags, "reading ElastiCache Cache Cluster (%s): %s", d.Id(), err)
	}

	d.Set("log_delivery_configuration", flattenLogDeliveryConfigurations(c.LogDeliveryConfigurations))
	d.Set("snapshot_window", c.SnapshotWindow)
	d.Set("snapshot_retention_limit", c.SnapshotRetentionLimit)

	d.Set("num_cache_nodes", c.NumCacheNodes)

	if c.ConfigurationEndpoint != nil {
		d.Set(names.AttrPort, c.ConfigurationEndpoint.Port)
		d.Set("configuration_endpoint", aws.String(fmt.Sprintf("%s:%d", aws.StringValue(c.ConfigurationEndpoint.Address), aws.Int64Value(c.ConfigurationEndpoint.Port))))
		d.Set("cluster_address", c.ConfigurationEndpoint.Address)
	} else if len(c.CacheNodes) > 0 {
		d.Set(names.AttrPort, c.CacheNodes[0].Endpoint.Port)
	}

	d.Set("replication_group_id", c.ReplicationGroupId)

	if c.NotificationConfiguration != nil {
		if aws.StringValue(c.NotificationConfiguration.TopicStatus) == "active" {
			d.Set("notification_topic_arn", c.NotificationConfiguration.TopicArn)
		}
	}
	d.Set(names.AttrAvailabilityZone, c.PreferredAvailabilityZone)
	if aws.StringValue(c.PreferredAvailabilityZone) == "Multiple" {
		d.Set("az_mode", "cross-az")
	} else {
		d.Set("az_mode", "single-az")
	}

	if err := setCacheNodeData(d, c); err != nil {
		return sdkdiag.AppendErrorf(diags, "reading ElastiCache Cache Cluster (%s): %s", d.Id(), err)
	}

	d.Set(names.AttrARN, c.ARN)

	d.Set("ip_discovery", c.IpDiscovery)
	d.Set("network_type", c.NetworkType)
	d.Set("preferred_outpost_arn", c.PreferredOutpostArn)
	d.Set("transit_encryption_enabled", c.TransitEncryptionEnabled)

	return diags
}

func resourceClusterUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ElastiCacheConn(ctx)

	if d.HasChangesExcept(names.AttrTags, names.AttrTagsAll) {
		input := &elasticache.ModifyCacheClusterInput{
			CacheClusterId:   aws.String(d.Id()),
			ApplyImmediately: aws.Bool(d.Get("apply_immediately").(bool)),
		}

		requestUpdate := false
		if d.HasChange(names.AttrSecurityGroupIDs) {
			if attr := d.Get(names.AttrSecurityGroupIDs).(*schema.Set); attr.Len() > 0 {
				input.SecurityGroupIds = flex.ExpandStringSet(attr)
				requestUpdate = true
			}
		}

		if d.HasChange("parameter_group_name") {
			input.CacheParameterGroupName = aws.String(d.Get("parameter_group_name").(string))
			requestUpdate = true
		}

		if d.HasChange("ip_discovery") {
			input.IpDiscovery = aws.String(d.Get("ip_discovery").(string))
			requestUpdate = true
		}

		if d.HasChange("log_delivery_configuration") {
			oldLogDeliveryConfig, newLogDeliveryConfig := d.GetChange("log_delivery_configuration")

			input.LogDeliveryConfigurations = []*elasticache.LogDeliveryConfigurationRequest{}
			logTypesToSubmit := make(map[string]bool)

			currentLogDeliveryConfig := newLogDeliveryConfig.(*schema.Set).List()
			for _, current := range currentLogDeliveryConfig {
				logDeliveryConfigurationRequest := expandLogDeliveryConfigurations(current.(map[string]interface{}))
				logTypesToSubmit[*logDeliveryConfigurationRequest.LogType] = true
				input.LogDeliveryConfigurations = append(input.LogDeliveryConfigurations, &logDeliveryConfigurationRequest)
			}

			previousLogDeliveryConfig := oldLogDeliveryConfig.(*schema.Set).List()
			for _, previous := range previousLogDeliveryConfig {
				logDeliveryConfigurationRequest := expandEmptyLogDeliveryConfigurations(previous.(map[string]interface{}))
				// if something was removed, send an empty request
				if !logTypesToSubmit[*logDeliveryConfigurationRequest.LogType] {
					input.LogDeliveryConfigurations = append(input.LogDeliveryConfigurations, &logDeliveryConfigurationRequest)
				}
			}

			requestUpdate = true
		}

		if d.HasChange("maintenance_window") {
			input.PreferredMaintenanceWindow = aws.String(d.Get("maintenance_window").(string))
			requestUpdate = true
		}

		if d.HasChange("notification_topic_arn") {
			v := d.Get("notification_topic_arn").(string)
			input.NotificationTopicArn = aws.String(v)
			if v == "" {
				inactive := "inactive"
				input.NotificationTopicStatus = &inactive
			}
			requestUpdate = true
		}

		if d.HasChange("engine_version") {
			input.EngineVersion = aws.String(d.Get("engine_version").(string))
			requestUpdate = true
		}

		if d.HasChange("auto_minor_version_upgrade") {
			v := d.Get("auto_minor_version_upgrade")
			if v, null, _ := nullable.Bool(v.(string)).ValueBool(); !null {
				input.AutoMinorVersionUpgrade = aws.Bool(v)
			}
			requestUpdate = true
		}

		if d.HasChange("snapshot_window") {
			input.SnapshotWindow = aws.String(d.Get("snapshot_window").(string))
			requestUpdate = true
		}

		if d.HasChange("node_type") {
			input.CacheNodeType = aws.String(d.Get("node_type").(string))
			requestUpdate = true
		}

		if d.HasChange("snapshot_retention_limit") {
			input.SnapshotRetentionLimit = aws.Int64(int64(d.Get("snapshot_retention_limit").(int)))
			requestUpdate = true
		}

		if d.HasChange("az_mode") {
			input.AZMode = aws.String(d.Get("az_mode").(string))
			requestUpdate = true
		}

		if d.HasChange("num_cache_nodes") {
			oraw, nraw := d.GetChange("num_cache_nodes")
			o := oraw.(int)
			n := nraw.(int)
			if n < o {
				log.Printf("[INFO] Cluster %s is marked for Decreasing cache nodes from %d to %d", d.Id(), o, n)
				nodesToRemove := getCacheNodesToRemove(o, o-n)
				input.CacheNodeIdsToRemove = nodesToRemove
			} else {
				log.Printf("[INFO] Cluster %s is marked for increasing cache nodes from %d to %d", d.Id(), o, n)
				// SDK documentation for NewAvailabilityZones states:
				// The list of Availability Zones where the new Memcached cache nodes are created.
				//
				// This parameter is only valid when NumCacheNodes in the request is greater
				// than the sum of the number of active cache nodes and the number of cache
				// nodes pending creation (which may be zero). The number of Availability Zones
				// supplied in this list must match the cache nodes being added in this request.
				if v, ok := d.GetOk("preferred_availability_zones"); ok && len(v.([]interface{})) > 0 {
					// Here we check the list length to prevent a potential panic :)
					if len(v.([]interface{})) != n {
						return sdkdiag.AppendErrorf(diags, "length of preferred_availability_zones (%d) must match num_cache_nodes (%d)", len(v.([]interface{})), n)
					}
					input.NewAvailabilityZones = flex.ExpandStringList(v.([]interface{})[o:])
				}
			}

			input.NumCacheNodes = aws.Int64(int64(d.Get("num_cache_nodes").(int)))
			requestUpdate = true
		}

		if requestUpdate {
			log.Printf("[DEBUG] Modifying ElastiCache Cluster (%s), opts:\n%s", d.Id(), input)
			_, err := conn.ModifyCacheClusterWithContext(ctx, input)
			if err != nil {
				return sdkdiag.AppendErrorf(diags, "updating ElastiCache cluster (%s), error: %s", d.Id(), err)
			}

			const (
				timeout = 80 * time.Minute
			)
			_, err = waitCacheClusterAvailable(ctx, conn, d.Id(), timeout)
			if err != nil {
				return sdkdiag.AppendErrorf(diags, "waiting for ElastiCache Cache Cluster (%s) to update: %s", d.Id(), err)
			}
		}
	}

	return append(diags, resourceClusterRead(ctx, d, meta)...)
}

func resourceClusterDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ElastiCacheConn(ctx)

	var finalSnapshotID = d.Get("final_snapshot_identifier").(string)
	err := DeleteCacheCluster(ctx, conn, d.Id(), finalSnapshotID)
	if err != nil {
		if tfawserr.ErrCodeEquals(err, elasticache.ErrCodeCacheClusterNotFoundFault) {
			return diags
		}
		return sdkdiag.AppendErrorf(diags, "deleting ElastiCache Cache Cluster (%s): %s", d.Id(), err)
	}
	const (
		timeout = 40 * time.Minute
	)
	_, err = waitCacheClusterDeleted(ctx, conn, d.Id(), timeout)
	if err != nil {
		return sdkdiag.AppendErrorf(diags, "waiting for ElastiCache Cache Cluster (%s) to be deleted: %s", d.Id(), err)
	}

	return diags
}

func createCacheCluster(ctx context.Context, conn *elasticache.ElastiCache, input *elasticache.CreateCacheClusterInput) (string, string, error) {
	output, err := conn.CreateCacheClusterWithContext(ctx, input)

	// Some partitions (e.g. ISO) may not support tag-on-create.
	if input.Tags != nil && errs.IsUnsupportedOperationInPartitionError(conn.PartitionID, err) {
		input.Tags = nil

		output, err = conn.CreateCacheClusterWithContext(ctx, input)
	}

	if err != nil {
		return "", "", err
	}

	if output == nil || output.CacheCluster == nil {
		return "", "", errors.New("missing cluster ID after creation")
	}
	// ElastiCache always retains the id in lower case, so we have to
	// mimic that or else we won't be able to refresh a resource whose
	// name contained uppercase characters.
	return strings.ToLower(aws.StringValue(output.CacheCluster.CacheClusterId)), aws.StringValue(output.CacheCluster.ARN), nil
}

func DeleteCacheCluster(ctx context.Context, conn *elasticache.ElastiCache, cacheClusterID string, finalSnapshotID string) error {
	input := &elasticache.DeleteCacheClusterInput{
		CacheClusterId: aws.String(cacheClusterID),
	}
	if finalSnapshotID != "" {
		input.FinalSnapshotIdentifier = aws.String(finalSnapshotID)
	}

	log.Printf("[DEBUG] Deleting ElastiCache Cache Cluster: %s", input)
	err := retry.RetryContext(ctx, 5*time.Minute, func() *retry.RetryError {
		_, err := conn.DeleteCacheClusterWithContext(ctx, input)
		if err != nil {
			if tfawserr.ErrMessageContains(err, elasticache.ErrCodeInvalidCacheClusterStateFault, "serving as primary") {
				return retry.NonRetryableError(err)
			}
			if tfawserr.ErrMessageContains(err, elasticache.ErrCodeInvalidCacheClusterStateFault, "only member of a replication group") {
				return retry.NonRetryableError(err)
			}
			// The cluster may be just snapshotting, so we retry until it's ready for deletion
			if tfawserr.ErrCodeEquals(err, elasticache.ErrCodeInvalidCacheClusterStateFault) {
				return retry.RetryableError(err)
			}
			return retry.NonRetryableError(err)
		}
		return nil
	})
	if tfresource.TimedOut(err) {
		_, err = conn.DeleteCacheClusterWithContext(ctx, input)
	}

	return err
}

func findCacheClusterByID(ctx context.Context, conn *elasticache.ElastiCache, id string) (*elasticache.CacheCluster, error) {
	input := &elasticache.DescribeCacheClustersInput{
		CacheClusterId: aws.String(id),
	}

	return findCacheCluster(ctx, conn, input, tfslices.PredicateTrue[*elasticache.CacheCluster]())
}
func findCacheClusterWithNodeInfoByID(ctx context.Context, conn *elasticache.ElastiCache, id string) (*elasticache.CacheCluster, error) {
	input := &elasticache.DescribeCacheClustersInput{
		CacheClusterId:    aws.String(id),
		ShowCacheNodeInfo: aws.Bool(true),
	}

	return findCacheCluster(ctx, conn, input, tfslices.PredicateTrue[*elasticache.CacheCluster]())
}

func findCacheCluster(ctx context.Context, conn *elasticache.ElastiCache, input *elasticache.DescribeCacheClustersInput, filter tfslices.Predicate[*elasticache.CacheCluster]) (*elasticache.CacheCluster, error) {
	output, err := findCacheClusters(ctx, conn, input, filter)

	if err != nil {
		return nil, err
	}

	return tfresource.AssertSinglePtrResult(output)
}

func findCacheClusters(ctx context.Context, conn *elasticache.ElastiCache, input *elasticache.DescribeCacheClustersInput, filter tfslices.Predicate[*elasticache.CacheCluster]) ([]*elasticache.CacheCluster, error) {
	var output []*elasticache.CacheCluster

	err := conn.DescribeCacheClustersPagesWithContext(ctx, input, func(page *elasticache.DescribeCacheClustersOutput, lastPage bool) bool {
		if page == nil {
			return !lastPage
		}

		for _, v := range page.CacheClusters {
			if v != nil && filter(v) {
				output = append(output, v)
			}
		}

		return !lastPage
	})

	if tfawserr.ErrCodeEquals(err, elasticache.ErrCodeCacheClusterNotFoundFault) {
		return nil, &retry.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}

	if err != nil {
		return nil, err
	}

	return output, nil
}

func statusCacheCluster(ctx context.Context, conn *elasticache.ElastiCache, cacheClusterID string) retry.StateRefreshFunc {
	return func() (interface{}, string, error) {
		output, err := findCacheClusterByID(ctx, conn, cacheClusterID)

		if tfresource.NotFound(err) {
			return nil, "", nil
		}

		if err != nil {
			return nil, "", err
		}

		return output, aws.StringValue(output.CacheClusterStatus), nil
	}
}

const (
	cacheClusterStatusAvailable             = "available"
	cacheClusterStatusCreating              = "creating"
	cacheClusterStatusDeleted               = "deleted"
	cacheClusterStatusDeleting              = "deleting"
	cacheClusterStatusIncompatibleNetwork   = "incompatible-network"
	cacheClusterStatusModifying             = "modifying"
	cacheClusterStatusRebootingClusterNodes = "rebooting cluster nodes"
	cacheClusterStatusRestoreFailed         = "restore-failed"
	cacheClusterStatusSnapshotting          = "snapshotting"
)

func waitCacheClusterAvailable(ctx context.Context, conn *elasticache.ElastiCache, cacheClusterID string, timeout time.Duration) (*elasticache.CacheCluster, error) { //nolint:unparam
	stateConf := &retry.StateChangeConf{
		Pending: []string{
			cacheClusterStatusCreating,
			cacheClusterStatusModifying,
			cacheClusterStatusSnapshotting,
			cacheClusterStatusRebootingClusterNodes,
		},
		Target:     []string{cacheClusterStatusAvailable},
		Refresh:    statusCacheCluster(ctx, conn, cacheClusterID),
		Timeout:    timeout,
		MinTimeout: 10 * time.Second,
		Delay:      30 * time.Second,
	}

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*elasticache.CacheCluster); ok {
		return output, err
	}

	return nil, err
}

func waitCacheClusterDeleted(ctx context.Context, conn *elasticache.ElastiCache, cacheClusterID string, timeout time.Duration) (*elasticache.CacheCluster, error) {
	stateConf := &retry.StateChangeConf{
		Pending: []string{
			cacheClusterStatusCreating,
			cacheClusterStatusAvailable,
			cacheClusterStatusModifying,
			cacheClusterStatusDeleting,
			cacheClusterStatusIncompatibleNetwork,
			cacheClusterStatusRestoreFailed,
			cacheClusterStatusSnapshotting,
		},
		Target:     []string{},
		Refresh:    statusCacheCluster(ctx, conn, cacheClusterID),
		Timeout:    timeout,
		MinTimeout: 10 * time.Second,
		Delay:      30 * time.Second,
	}

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*elasticache.CacheCluster); ok {
		return output, err
	}

	return nil, err
}

func getCacheNodesToRemove(oldNumberOfNodes int, cacheNodesToRemove int) []*string {
	nodesIdsToRemove := []*string{}
	for i := oldNumberOfNodes; i > oldNumberOfNodes-cacheNodesToRemove && i > 0; i-- {
		s := fmt.Sprintf("%04d", i)
		nodesIdsToRemove = append(nodesIdsToRemove, &s)
	}

	return nodesIdsToRemove
}

func setCacheNodeData(d *schema.ResourceData, c *elasticache.CacheCluster) error {
	sortedCacheNodes := make([]*elasticache.CacheNode, len(c.CacheNodes))
	copy(sortedCacheNodes, c.CacheNodes)
	sort.Sort(byCacheNodeId(sortedCacheNodes))

	cacheNodeData := make([]map[string]interface{}, 0, len(sortedCacheNodes))

	for _, node := range sortedCacheNodes {
		if node.CacheNodeId == nil || node.Endpoint == nil || node.Endpoint.Address == nil || node.Endpoint.Port == nil || node.CustomerAvailabilityZone == nil {
			return fmt.Errorf("Unexpected nil pointer in: %s", node)
		}
		cacheNodeData = append(cacheNodeData, map[string]interface{}{
			names.AttrID:               aws.StringValue(node.CacheNodeId),
			"address":                  aws.StringValue(node.Endpoint.Address),
			names.AttrPort:             aws.Int64Value(node.Endpoint.Port),
			names.AttrAvailabilityZone: aws.StringValue(node.CustomerAvailabilityZone),
			"outpost_arn":              aws.StringValue(node.CustomerOutpostArn),
		})
	}

	return d.Set("cache_nodes", cacheNodeData)
}

type byCacheNodeId []*elasticache.CacheNode

func (b byCacheNodeId) Len() int      { return len(b) }
func (b byCacheNodeId) Swap(i, j int) { b[i], b[j] = b[j], b[i] }
func (b byCacheNodeId) Less(i, j int) bool {
	return b[i].CacheNodeId != nil && b[j].CacheNodeId != nil &&
		aws.StringValue(b[i].CacheNodeId) < aws.StringValue(b[j].CacheNodeId)
}

func setFromCacheCluster(d *schema.ResourceData, c *elasticache.CacheCluster) error {
	d.Set("node_type", c.CacheNodeType)

	d.Set("engine", c.Engine)
	if aws.StringValue(c.Engine) == engineRedis {
		if err := setEngineVersionRedis(d, c.EngineVersion); err != nil {
			return err // nosemgrep:ci.bare-error-returns
		}
	} else {
		setEngineVersionMemcached(d, c.EngineVersion)
	}
	d.Set("auto_minor_version_upgrade", strconv.FormatBool(aws.BoolValue(c.AutoMinorVersionUpgrade)))

	d.Set("subnet_group_name", c.CacheSubnetGroupName)
	if err := d.Set(names.AttrSecurityGroupIDs, flattenSecurityGroupIDs(c.SecurityGroups)); err != nil {
		return fmt.Errorf("setting security_group_ids: %w", err)
	}

	if c.CacheParameterGroup != nil {
		d.Set("parameter_group_name", c.CacheParameterGroup.CacheParameterGroupName)
	}

	d.Set("maintenance_window", c.PreferredMaintenanceWindow)

	return nil
}
