package collection

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

// tuneShared applies the settings every collector takes. The profiler keeps
// its own metadata list.
func tuneShared(c map[string]interface{}, t v1alpha1.CollectorTuning, cfg v1alpha1.TsugaCollectorConfigSpec, isProfiler bool) {
	if t.HealthCheckEndpoint != nil {
		setHealthCheckEndpoint(c, *t.HealthCheckEndpoint)
	}
	extract := k8sAttributesExtract(c)
	if extract != nil {
		appendMappings(extract, "labels", t.ExtraLabelMapping)
		appendMappings(extract, "annotations", t.ExtraAnnotationsMapping)
		if !isProfiler && len(cfg.K8sAttributes.Metadata) > 0 {
			extract["metadata"] = stringsToSlice(cfg.K8sAttributes.Metadata)
		}
	}
	tuneBatch(c, cfg.Batch)
}

var builtinFileLogExcludes = []string{
	"/var/log/pods/*/otc-container/*.log",
	"/var/log/pods/*/otel-collector/*.log",
}

func agentMountsLogs(cfg v1alpha1.TsugaCollectorConfigSpec) bool {
	return ptr.Deref(cfg.Agent.CollectLogs, true) || ptr.Deref(cfg.Agent.AddLogsVolumes, false)
}

func tuneAgentConfig(c map[string]interface{}, cfg v1alpha1.TsugaCollectorConfigSpec) error {
	a := cfg.Agent
	tuneShared(c, a.CollectorTuning, cfg, false)
	receivers := nestedMap(c, "receivers")
	tuneAgentOTLP(receivers, a.OTLP)
	if !ptr.Deref(a.CollectLogs, true) {
		dropFileLog(c)
	} else {
		tuneFileLog(receivers, a)
	}
	if err := tuneHostMetrics(receivers, a); err != nil {
		return err
	}
	tuneKubeletStats(receivers, a.KubeletStats)
	if err := tuneSpanMetrics(c, a.SpanMetrics); err != nil {
		return err
	}
	pruneUnreferencedReceivers(c)
	tuneExport(c, a.CollectorTuning, cfg.Export, false)
	return nil
}

func tuneAgentOTLP(receivers map[string]interface{}, o v1alpha1.AgentOTLP) {
	protocols := nestedMap(nestedMap(receivers, "otlp"), "protocols")
	for protocol, endpoint := range map[string]string{"grpc": o.GRPCEndpoint, "http": o.HTTPEndpoint} {
		if endpoint == "" {
			continue
		}
		if p, ok := asMap(protocols[protocol]); ok {
			p["endpoint"] = endpoint
		}
	}
}

func dropFileLog(c map[string]interface{}) {
	pipelines := asMapOrEmpty(pipelinesOf(c))
	logs, ok := asMap(pipelines["logs"])
	if !ok {
		return
	}
	remaining := stripName(logs["receivers"], "file_log")
	if len(remaining) == 0 {
		delete(pipelines, "logs")
		return
	}
	logs["receivers"] = remaining
}

func tuneFileLog(receivers map[string]interface{}, a v1alpha1.TelemetryToggles) {
	fileLog, ok := asMap(receivers["file_log"])
	if !ok {
		return
	}
	if len(a.FileLog.Include) > 0 {
		fileLog["include"] = stringsToSlice(a.FileLog.Include)
	}
	if a.CollectOtelLogs || len(a.FileLog.Exclude) > 0 {
		exclude := stringSlice(fileLog["exclude"])
		if a.CollectOtelLogs {
			exclude = slices.DeleteFunc(exclude, func(p string) bool { return slices.Contains(builtinFileLogExcludes, p) })
		}
		fileLog["exclude"] = stringsToSlice(append(exclude, a.FileLog.Exclude...))
	}
}

func tuneHostMetrics(receivers map[string]interface{}, a v1alpha1.TelemetryToggles) error {
	hostMetrics, ok := asMap(receivers["host_metrics"])
	if !ok {
		return nil
	}
	if a.HostMetrics.CollectionInterval != "" {
		hostMetrics["collection_interval"] = a.HostMetrics.CollectionInterval
	}
	if a.HostMetrics.Scrapers != nil {
		scrapers := map[string]interface{}{}
		if err := json.Unmarshal(a.HostMetrics.Scrapers.Raw, &scrapers); err != nil {
			return fmt.Errorf("agent.hostMetrics.scrapers: %w", err)
		}
		maps.DeleteFunc(scrapers, func(_ string, v interface{}) bool { return v == nil })
		if len(scrapers) == 0 {
			return errors.New("agent.hostMetrics.scrapers is empty: the host_metrics receiver needs at least one scraper")
		}
		hostMetrics["scrapers"] = scrapers
		return nil
	}
	scrapers := nestedMap(hostMetrics, "scrapers")
	if a.CollectNetwork {
		scrapers["network"] = map[string]interface{}{"metrics": enabledMetrics("system.network.conntrack.count", "system.network.conntrack.max")}
	}
	if a.CollectProcesses {
		scrapers["process"] = map[string]interface{}{
			"mute_process_all_errors": true,
			"resource_attributes":     map[string]interface{}{"process.executable.path": map[string]interface{}{"enabled": false}},
			"metrics":                 enabledMetrics("process.uptime"),
		}
		scrapers["processes"] = map[string]interface{}{}
	}
	return nil
}

func enabledMetrics(names ...string) map[string]interface{} {
	metrics := map[string]interface{}{}
	for _, n := range names {
		metrics[n] = map[string]interface{}{"enabled": true}
	}
	return metrics
}

func tuneKubeletStats(receivers map[string]interface{}, k v1alpha1.KubeletStatsSpec) {
	kubelet, ok := asMap(receivers["kubelet_stats"])
	if !ok {
		return
	}
	if k.CollectionInterval != "" {
		kubelet["collection_interval"] = k.CollectionInterval
	}
	if k.InsecureSkipVerify != nil {
		kubelet["insecure_skip_verify"] = *k.InsecureSkipVerify
	}
	if k.CAFile != "" {
		kubelet["ca_file"] = k.CAFile
	}
	if len(k.MetricGroups) > 0 {
		kubelet["metric_groups"] = stringsToSlice(k.MetricGroups)
	}
	if !ptr.Deref(k.UsePodsEndpoint, true) {
		delete(kubelet, "extra_metadata_labels")
		metrics := nestedMap(kubelet, "metrics")
		for _, kind := range []string{"container", "pod"} {
			for _, resource := range []string{"cpu", "memory"} {
				for _, suffix := range []string{"limit", "request"} {
					delete(metrics, fmt.Sprintf("k8s.%s.%s_%s_utilization", kind, resource, suffix))
				}
			}
		}
	}
}

func tuneSpanMetrics(c map[string]interface{}, s v1alpha1.SpanMetricsSpec) error {
	connectors := nestedMap(c, "connectors")
	if !ptr.Deref(s.Enabled, true) {
		removePipelineReference(asMapOrEmpty(pipelinesOf(c)), "span_metrics")
		return nil
	}
	connector, ok := asMap(connectors["span_metrics"])
	if !ok {
		return nil
	}
	if s.AggregationTemporality != "" {
		connector["aggregation_temporality"] = s.AggregationTemporality
	}
	if len(s.Dimensions) > 0 {
		dims, err := toUnstructuredSlice(s.Dimensions)
		if err != nil {
			return fmt.Errorf("convert spanMetrics dimensions: %w", err)
		}
		connector["dimensions"] = dims
	}
	return nil
}

func tuneGatewayConfig(c map[string]interface{}, cfg v1alpha1.TsugaCollectorConfigSpec) error {
	tuneShared(c, cfg.Gateway.CollectorTuning, cfg, false)
	tuneExport(c, cfg.Gateway.CollectorTuning, cfg.Export, false)
	cluster, ok := asMap(nestedMap(c, "receivers")["k8s_cluster"])
	if !ok {
		return nil
	}
	if cfg.Gateway.CollectionInterval != "" {
		cluster["collection_interval"] = cfg.Gateway.CollectionInterval
	}
	if len(cfg.Gateway.NodeConditionsToReport) > 0 {
		cluster["node_conditions_to_report"] = stringsToSlice(cfg.Gateway.NodeConditionsToReport)
	}
	if len(cfg.Gateway.AllocatableTypesToReport) > 0 {
		cluster["allocatable_types_to_report"] = stringsToSlice(cfg.Gateway.AllocatableTypesToReport)
	}
	return nil
}

func tuneScraperConfig(c map[string]interface{}, cfg v1alpha1.TsugaCollectorConfigSpec) error {
	tuneShared(c, cfg.Prometheus.CollectorTuning, cfg, false)
	tuneExport(c, cfg.Prometheus.CollectorTuning, cfg.Export, false)
	if ptr.Deref(cfg.Prometheus.AnnotationDiscovery, true) {
		return nil
	}
	// The OTel Operator moves scrape configs to the Target Allocator only when
	// there is at least one. This job scrapes nothing and reports up=0.
	prom := nestedMap(nestedMap(nestedMap(c, "receivers"), "prometheus"), "config")
	prom["scrape_configs"] = []interface{}{map[string]interface{}{
		"job_name":        "tsuga-placeholder",
		"scrape_interval": cfg.Prometheus.ScrapeInterval,
		"static_configs": []interface{}{
			map[string]interface{}{"targets": []interface{}{"localhost:8888"}},
		},
	}}
	return nil
}

func tuneProfilerConfig(c map[string]interface{}, cfg v1alpha1.TsugaCollectorConfigSpec) error {
	p := cfg.Profiling
	tuneShared(c, p.CollectorTuning, cfg, true)
	tuneExport(c, p.CollectorTuning, cfg.Export, true)
	if receiver, ok := asMap(nestedMap(c, "receivers")["profiling"]); ok {
		if p.ServiceNameEnvVar != "" {
			receiver["include_env_vars"] = p.ServiceNameEnvVar
		}
	}
	if extract := k8sAttributesExtract(c); extract != nil && len(p.K8sAttributesMetadata) > 0 {
		extract["metadata"] = stringsToSlice(p.K8sAttributesMetadata)
	}
	if p.ServiceNameEnvVar != "" {
		addServiceNameTransform(c, p.ServiceNameEnvVar)
	}
	return nil
}

const tsugaExporter = "otlp_http/tsuga"

// tuneExport applies the export options to one collector: encoding and
// compression on the Tsuga exporter, and removal of
// everything that talks to Tsuga when exportToTsuga is off. The profiles
// intake accepts only proto, so the profiler keeps its encoding.
func tuneExport(c map[string]interface{}, t v1alpha1.CollectorTuning, export *v1alpha1.ExportSpec, isProfiler bool) {
	exporters := nestedMap(c, "exporters")
	if tsuga, ok := asMap(exporters[tsugaExporter]); ok && export != nil {
		if export.Encoding != "" && !isProfiler {
			tsuga["encoding"] = export.Encoding
		}
		if export.Compression != "" {
			tsuga["compression"] = export.Compression
		}
	}

	if !ptr.Deref(t.ExportToTsuga, true) {
		delete(exporters, tsugaExporter)
		removePipelineReference(asMapOrEmpty(pipelinesOf(c)), tsugaExporter)
		// Self-telemetry readers push to the Tsuga endpoint. An empty metrics
		// block would fall back to a default pull reader on :8888.
		if telemetry, ok := asMap(nestedMap(c, "service")["telemetry"]); ok {
			telemetry["metrics"] = map[string]interface{}{"level": "none"}
		}
	}
}

// addServiceNameTransform promotes the profiled process's own variable to
// service.name. It sits right after memory_limiter, ahead of k8s_attributes,
// because k8s_attributes only fills attributes that are not already set.
func addServiceNameTransform(c map[string]interface{}, envVar string) {
	attr := fmt.Sprintf(`resource.attributes["process.environment_variable.%s"]`, envVar)
	nestedMap(c, "processors")["transform/service_name"] = map[string]interface{}{
		"error_mode": "ignore",
		"profile_statements": []interface{}{
			map[string]interface{}{
				"context": "resource",
				"statements": []interface{}{
					fmt.Sprintf(`set(resource.attributes["service.name"], %[1]s) where %[1]s != nil and %[1]s != ""`, attr),
					fmt.Sprintf(`delete_key(resource.attributes, "process.environment_variable.%s")`, envVar),
				},
			},
		},
	}
	pipeline, ok := asMap(asMapOrEmpty(pipelinesOf(c))["profiles"])
	if !ok {
		return
	}
	pipeline["processors"] = insertAfter(sliceOf(pipeline["processors"]), []string{"memory_limiter", "resource_detection"}, "transform/service_name")
}

// tuneEnrichment adds the resource_detection processor to every pipeline of
// every collector and, on the agent, the redaction processor before batch (or
// last, in a pipeline without batch). It runs after tuneExtras so
// extraPipelines are covered too.
func tuneEnrichment(c map[string]interface{}, cfg v1alpha1.TsugaCollectorConfigSpec, isAgent bool) error {
	pipelines := asMapOrEmpty(pipelinesOf(c))
	if rd := cfg.ResourceDetection; rd.Enabled {
		detectors := rd.Detectors
		if len(detectors) == 0 {
			detectors = []string{"env"}
		}
		nestedMap(c, "processors")["resource_detection"] = map[string]interface{}{
			"detectors": stringsToSlice(detectors),
			"timeout":   cmp.Or(rd.Timeout, "15s"),
			"override":  false,
		}
		for _, raw := range pipelines {
			if pipeline, ok := asMap(raw); ok {
				pipeline["processors"] = insertAfter(sliceOf(pipeline["processors"]), []string{"memory_limiter", "cumulative_to_delta"}, "resource_detection")
			}
		}
	}
	if !isAgent || !cfg.Redaction.Enabled {
		return nil
	}
	redaction, err := redactionConfig(cfg.Redaction)
	if err != nil {
		return err
	}
	nestedMap(c, "processors")["redaction"] = redaction
	for _, raw := range pipelines {
		pipeline, ok := asMap(raw)
		if !ok {
			continue
		}
		list := sliceOf(pipeline["processors"])
		i := slices.Index(list, interface{}("batch"))
		if i < 0 {
			i = len(list)
		}
		pipeline["processors"] = slices.Insert(list, i, interface{}("redaction"))
	}
	return nil
}

func redactionConfig(r v1alpha1.RedactionSpec) (map[string]interface{}, error) {
	config := map[string]interface{}{}
	var err error
	if r.Config != nil {
		err = json.Unmarshal(r.Config.Raw, &config)
	} else {
		err = yaml.Unmarshal([]byte(assets.RedactionDefault), &config)
	}
	if err != nil {
		return nil, fmt.Errorf("redaction.config: %w", err)
	}
	if config["allow_all_keys"] != true && len(sliceOf(config["allowed_keys"])) == 0 {
		return nil, errors.New("redaction config would delete every attribute; set allow_all_keys: true or list allowed_keys")
	}
	return config, nil
}

// insertAfter places name after the leading run of anchors (memory_limiter and
// whatever must precede enrichment), so the result does not depend on which
// enrichment step runs first.
func insertAfter(processors []interface{}, anchors []string, name string) []interface{} {
	i := 0
	for i < len(processors) {
		p, _ := processors[i].(string)
		if !slices.Contains(anchors, p) {
			break
		}
		i++
	}
	return slices.Insert(processors, i, interface{}(name))
}

func setHealthCheckEndpoint(c map[string]interface{}, endpoint string) {
	if endpoint != "" {
		extensions := nestedMap(c, "extensions")
		extensions["health_check"] = map[string]interface{}{"endpoint": endpoint}
		return
	}
	delete(nestedMap(c, "extensions"), "health_check")
	service := nestedMap(c, "service")
	if remaining := stripName(service["extensions"], "health_check"); len(remaining) > 0 {
		service["extensions"] = remaining
	} else {
		delete(service, "extensions")
	}
}

func tuneBatch(c map[string]interface{}, b v1alpha1.BatchSpec) {
	batch, ok := asMap(nestedMap(c, "processors")["batch"])
	if !ok {
		return
	}
	if b.SendBatchSize != nil {
		batch["send_batch_size"] = int64(*b.SendBatchSize)
	}
	if b.SendBatchMaxSize != nil {
		batch["send_batch_max_size"] = int64(*b.SendBatchMaxSize)
	}
	if b.Timeout != "" {
		batch["timeout"] = b.Timeout
	}
}

func k8sAttributesExtract(c map[string]interface{}) map[string]interface{} {
	extract, _ := asMap(asMapOrEmpty(nestedMap(c, "processors")["k8s_attributes"])["extract"])
	return extract
}

func appendMappings(extract map[string]interface{}, key string, in []v1alpha1.AttributeMapping) {
	out := sliceOf(extract[key])
	for _, m := range in {
		out = append(out, map[string]interface{}{"tag_name": m.TagName, "key": m.Key, "from": cmp.Or(m.From, "pod")})
	}
	if len(in) > 0 {
		extract[key] = out
	}
}

func stringsToSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func sliceOf(v interface{}) []interface{} {
	list, _ := v.([]interface{})
	return list
}

func asMapOrEmpty(v interface{}) map[string]interface{} {
	m, _ := asMap(v)
	return m
}

// AgentEnabled reports whether the node agent is rendered.
func AgentEnabled(cfg v1alpha1.TsugaCollectorConfigSpec) bool {
	return ptr.Deref(cfg.Agent.Enabled, true)
}

// AgentOTLPPort returns the port the agent's OTLP receiver for protocol
// ("http" or "grpc") listens on.
func AgentOTLPPort(cfg v1alpha1.TsugaCollectorConfigSpec, protocol string) int32 {
	endpoint, builtin := cfg.Agent.OTLP.HTTPEndpoint, int32(4318)
	if protocol == "grpc" {
		endpoint, builtin = cfg.Agent.OTLP.GRPCEndpoint, 4317
	}
	port, err := strconv.ParseUint(endpoint[strings.LastIndex(endpoint, ":")+1:], 10, 16)
	if err != nil || port == 0 {
		return builtin
	}
	return int32(port)
}
