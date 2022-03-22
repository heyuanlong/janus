package cmd

import (
	"fmt"
	"github.com/hellofresh/janus/loghook"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"contrib.go.opencensus.io/exporter/jaeger"
	"contrib.go.opencensus.io/exporter/prometheus"
	"github.com/hellofresh/janus/pkg/config"
	obs "github.com/hellofresh/janus/pkg/observability"
	"github.com/hellofresh/logging-go"
	_trace "github.com/hellofresh/opencensus-go-extras/trace"
	"github.com/hellofresh/stats-go"
	"github.com/hellofresh/stats-go/bucket"
	"github.com/hellofresh/stats-go/client"
	"github.com/hellofresh/stats-go/hooks"
	log "github.com/sirupsen/logrus"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"


)

var (
	globalConfig *config.Specification
	statsClient  client.Client
)

//初始化配置log
func initLogWriterEarly() {
	switch logging.LogWriter(strings.ToLower(os.Getenv("LOG_WRITER"))) {
	case logging.StdOut:
		log.SetOutput(os.Stdout)
	case logging.Discard:
		log.SetOutput(ioutil.Discard)
	case logging.StdErr:
		fallthrough
	default:
		log.SetOutput(os.Stderr)
	}

	log.AddHook(loghook.NewContextHook())
}

//加载配置
func initConfig() {
	var err error
	globalConfig, err = config.Load(configFile)
	if nil != err {
		log.WithError(err).Info("Could not load configurations from file - trying environment configurations instead.")

		globalConfig, err = config.LoadEnv()
		if nil != err {
			log.WithError(err).Error("Could not load configurations from environment variables")
		}
	}
}

//根据配置进步设置log
// initializes the basic configuration for the log wrapper
func initLog() {
	err := globalConfig.Log.Apply()
	if nil != err {
		log.WithError(err).Fatal("Could not apply logging configurations")
	}
}

//初始化统计库，已被修改为忽略统计
func initStatsClient() {
	// FIXME: this causes application hang because we're in the locked log already
	//statsLog.SetHandler(func(msg string, fields map[string]interface{}, err error) {
	//	entry := log.WithFields(log.Fields(fields))
	//	if err == nil {
	//		entry.Warn(msg)
	//	} else {
	//		entry.WithError(err).Warn(msg)
	//	}
	//})

	sectionsTestsMap, err := bucket.ParseSectionsTestsMap(globalConfig.Stats.IDs)
	if err != nil {
		log.WithError(err).WithField("config", globalConfig.Stats.IDs).
			Error("Failed to parse stats second level IDs from env")
		sectionsTestsMap = map[bucket.PathSection]bucket.SectionTestDefinition{}
	}
	log.WithField("config", globalConfig.Stats.IDs).
		WithField("map", sectionsTestsMap.String()).
		Debug("Setting stats second level IDs")

	statsClient, err = stats.NewClient("noop://") //修改为忽略统计
	//statsClient, err = stats.NewClient(globalConfig.Stats.DSN)
	if err != nil {
		log.WithError(err).Fatal("Error initializing stats client")
	}

	statsClient.SetHTTPMetricCallback(bucket.NewHasIDAtSecondLevelCallback(&bucket.SecondLevelIDConfig{
		HasIDAtSecondLevel:    sectionsTestsMap,
		AutoDiscoverThreshold: globalConfig.Stats.AutoDiscoverThreshold,
		AutoDiscoverWhiteList: globalConfig.Stats.AutoDiscoverWhiteList,
	}))

	host, err := os.Hostname()
	if nil != err {
		host = "-unknown-"
	}

	_, appFile := filepath.Split(os.Args[0])
	statsClient.TrackMetric("app", bucket.MetricOperation{"init", host, appFile})

	log.AddHook(hooks.NewLogrusHook(statsClient, globalConfig.Stats.ErrorsSection))
}

//初始化统计导出器
func initStatsExporter() {
	var err error
	logger := log.WithField("stats.exporter", globalConfig.Stats.Exporter)

	// Register stats exporter according to config
	switch globalConfig.Stats.Exporter {
	case obs.Datadog:
		fallthrough
	case obs.Stackdriver:
		logger.Warn("Not implemented!")
		return
	case obs.Prometheus:
		err = initPrometheusExporter()
	default:
		logger.Info("Invalid or no stats exporter was specified")
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed initialising stats exporter")
		return
	}

	// Configure/Register stats views
	view.SetReportingPeriod(time.Second)

	vv := append(obs.AllViews)

	if err := view.Register(vv...); err != nil {
		log.WithError(err).Warn("Failed to register server views")
	}
}

//初始化统计导出器-Prometheus
func initPrometheusExporter() (err error) {
	obs.PrometheusExporter, err = prometheus.NewExporter(prometheus.Options{})
	if err != nil {
		log.WithError(err).Warn("Failed to create prometheus exporter")
	} else {
		view.RegisterExporter(obs.PrometheusExporter)
	}
	return err
}


//初始化链路追踪器
func initTracingExporter() {
	var err error
	logger := log.WithField("tracing.exporter", globalConfig.Tracing.Exporter)

	switch globalConfig.Tracing.Exporter {
	case obs.AzureMonitor:
		fallthrough
	case obs.Datadog:
		fallthrough
	case obs.Stackdriver:
		fallthrough
	case obs.Zipkin:
		logger.Warn("Not implemented!")
	case obs.Jaeger:
		err = initJaegerExporter()
	default:
		logger.Info("Invalid or no tracing exporter was specified")
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed initialising tracing exporter")
		return
	}

	var traceConfig trace.Config
	var sampler trace.Sampler
	logger = logger.WithField("tracing.samplingStrategy", globalConfig.Tracing.SamplingStrategy)

	switch globalConfig.Tracing.SamplingStrategy {
	case "always":
		sampler = trace.AlwaysSample()
	case "never":
		sampler = trace.NeverSample()
	case "probabilistic":
		sampler = trace.ProbabilitySampler(globalConfig.Tracing.SamplingParam)
	default:
		logger.Warn("Invalid tracing sampling strategy specified")
		return
	}

	if !globalConfig.Tracing.IsPublicEndpoint {
		sampler = _trace.RespectParentSampler(sampler)
	}

	traceConfig.DefaultSampler = sampler
	trace.ApplyConfig(traceConfig)
}

func initJaegerExporter() (err error) {
	jaegerURL := globalConfig.Tracing.JaegerTracing.SamplingServerURL
	if jaegerURL == "" {
		jaegerURL = fmt.Sprintf("%s:%s", globalConfig.Tracing.JaegerTracing.SamplingServerHost, globalConfig.Tracing.JaegerTracing.SamplingServerPort)
	}

	jaegerExporter, err := jaeger.NewExporter(jaeger.Options{
		AgentEndpoint: jaegerURL,
		ServiceName:   globalConfig.Tracing.ServiceName,
	})
	if err != nil {
		log.WithError(err).Warn("Failed to create jaeger exporter")
	} else {
		trace.RegisterExporter(jaegerExporter)
	}
	return err
}
