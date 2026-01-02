module go.opentelemetry.io/collector/custom

go 1.23.0

require (
	github.com/go-chi/chi/v5 v5.2.3
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/nacos-group/nacos-sdk-go/v2 v2.3.5
	github.com/open-telemetry/opentelemetry-collector-contrib/connector/spanmetricsconnector v0.120.0
	github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusremotewriteexporter v0.120.0
	github.com/redis/go-redis/v9 v9.17.2
	github.com/stretchr/testify v1.10.0
	go.opentelemetry.io/collector/component v0.120.0
	go.opentelemetry.io/collector/component/componentstatus v0.120.0
	go.opentelemetry.io/collector/component/componenttest v0.120.0
	go.opentelemetry.io/collector/config/configgrpc v0.120.0
	go.opentelemetry.io/collector/config/confighttp v0.120.0
	go.opentelemetry.io/collector/config/confignet v1.26.0
	go.opentelemetry.io/collector/confmap v1.26.0
	go.opentelemetry.io/collector/confmap/provider/envprovider v1.24.0
	go.opentelemetry.io/collector/confmap/provider/fileprovider v1.24.0
	go.opentelemetry.io/collector/confmap/provider/httpprovider v1.24.0
	go.opentelemetry.io/collector/confmap/provider/httpsprovider v1.24.0
	go.opentelemetry.io/collector/confmap/provider/yamlprovider v1.24.0
	go.opentelemetry.io/collector/connector v0.120.0
	go.opentelemetry.io/collector/connector/forwardconnector v0.120.0
	go.opentelemetry.io/collector/consumer v1.26.0
	go.opentelemetry.io/collector/consumer/consumertest v0.120.0
	go.opentelemetry.io/collector/exporter v0.120.0
	go.opentelemetry.io/collector/exporter/debugexporter v0.120.0
	go.opentelemetry.io/collector/exporter/otlpexporter v0.120.0
	go.opentelemetry.io/collector/exporter/otlphttpexporter v0.120.0
	go.opentelemetry.io/collector/extension v0.120.0
	go.opentelemetry.io/collector/extension/extensioncapabilities v0.120.0
	go.opentelemetry.io/collector/extension/memorylimiterextension v0.120.0
	go.opentelemetry.io/collector/extension/zpagesextension v0.120.0
	go.opentelemetry.io/collector/otelcol v0.120.0
	go.opentelemetry.io/collector/pdata v1.26.0
	go.opentelemetry.io/collector/processor v0.120.0
	go.opentelemetry.io/collector/processor/batchprocessor v0.120.0
	go.opentelemetry.io/collector/processor/memorylimiterprocessor v0.120.0
	go.opentelemetry.io/collector/processor/processortest v0.120.0
	go.opentelemetry.io/collector/receiver v0.120.0
	go.opentelemetry.io/collector/receiver/nopreceiver v0.120.0
	go.opentelemetry.io/collector/receiver/otlpreceiver v0.120.0
	go.uber.org/zap v1.27.0
	google.golang.org/grpc v1.70.0
)

require (
	github.com/alibabacloud-go/alibabacloud-gateway-pop v0.0.6 // indirect
	github.com/alibabacloud-go/alibabacloud-gateway-spi v0.0.5 // indirect
	github.com/alibabacloud-go/darabonba-array v0.1.0 // indirect
	github.com/alibabacloud-go/darabonba-encode-util v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-map v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-openapi/v2 v2.0.10 // indirect
	github.com/alibabacloud-go/darabonba-signature-util v0.0.7 // indirect
	github.com/alibabacloud-go/darabonba-string v1.0.2 // indirect
	github.com/alibabacloud-go/debug v1.0.1 // indirect
	github.com/alibabacloud-go/endpoint-util v1.1.0 // indirect
	github.com/alibabacloud-go/kms-20160120/v3 v3.2.3 // indirect
	github.com/alibabacloud-go/openapi-util v0.1.0 // indirect
	github.com/alibabacloud-go/tea v1.2.2 // indirect
	github.com/alibabacloud-go/tea-utils v1.4.4 // indirect
	github.com/alibabacloud-go/tea-utils/v2 v2.0.7 // indirect
	github.com/alibabacloud-go/tea-xml v1.1.3 // indirect
	github.com/aliyun/alibaba-cloud-sdk-go v1.61.1800 // indirect
	github.com/aliyun/alibabacloud-dkms-gcs-go-sdk v0.5.1 // indirect
	github.com/aliyun/alibabacloud-dkms-transfer-go-sdk v0.1.8 // indirect
	github.com/aliyun/aliyun-secretsmanager-client-go v1.1.5 // indirect
	github.com/aliyun/credentials-go v1.4.3 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clbanning/mxj/v2 v2.5.5 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/deckarep/golang-set v1.7.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/ebitengine/purego v0.8.2 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.8.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-viper/mapstructure/v2 v2.2.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/grafana/regexp v0.0.0-20240518133315-a468a5bfb3bc // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.25.1 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/jonboulle/clockwork v0.4.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/knadh/koanf/maps v0.1.1 // indirect
	github.com/knadh/koanf/providers/confmap v0.1.0 // indirect
	github.com/knadh/koanf/v2 v2.1.2 // indirect
	github.com/lightstep/go-expohisto v1.0.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mostynb/go-grpc-compression v1.2.3 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/internal/coreinternal v0.120.0 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/internal/pdatautil v0.120.0 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatautil v0.120.0 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/pkg/resourcetotelemetry v0.120.0 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/prometheus v0.120.0 // indirect
	github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/prometheusremotewrite v0.120.0 // indirect
	github.com/orcaman/concurrent-map v0.0.0-20210501183033-44dafcb38ecc // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/prometheus/client_golang v1.20.5 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.62.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/prometheus/prometheus v0.300.1 // indirect
	github.com/rs/cors v1.11.1 // indirect
	github.com/shirou/gopsutil/v4 v4.25.1 // indirect
	github.com/spf13/cobra v1.8.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tidwall/gjson v1.10.2 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/tinylru v1.1.0 // indirect
	github.com/tidwall/wal v1.1.8 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/collector v0.120.0 // indirect
	go.opentelemetry.io/collector/client v1.26.0 // indirect
	go.opentelemetry.io/collector/config/configauth v0.120.0 // indirect
	go.opentelemetry.io/collector/config/configcompression v1.26.0 // indirect
	go.opentelemetry.io/collector/config/configopaque v1.26.0 // indirect
	go.opentelemetry.io/collector/config/configretry v1.26.0 // indirect
	go.opentelemetry.io/collector/config/configtelemetry v0.120.0 // indirect
	go.opentelemetry.io/collector/config/configtls v1.26.0 // indirect
	go.opentelemetry.io/collector/confmap/xconfmap v0.120.0 // indirect
	go.opentelemetry.io/collector/connector/connectortest v0.120.0 // indirect
	go.opentelemetry.io/collector/connector/xconnector v0.120.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.120.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror/xconsumererror v0.120.0 // indirect
	go.opentelemetry.io/collector/consumer/xconsumer v0.120.0 // indirect
	go.opentelemetry.io/collector/exporter/exporterhelper/xexporterhelper v0.120.0 // indirect
	go.opentelemetry.io/collector/exporter/exportertest v0.120.0 // indirect
	go.opentelemetry.io/collector/exporter/xexporter v0.120.0 // indirect
	go.opentelemetry.io/collector/extension/auth v0.120.0 // indirect
	go.opentelemetry.io/collector/extension/extensiontest v0.120.0 // indirect
	go.opentelemetry.io/collector/extension/xextension v0.120.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.26.0 // indirect
	go.opentelemetry.io/collector/internal/fanoutconsumer v0.120.0 // indirect
	go.opentelemetry.io/collector/internal/memorylimiter v0.120.0 // indirect
	go.opentelemetry.io/collector/internal/sharedcomponent v0.120.0 // indirect
	go.opentelemetry.io/collector/internal/telemetry v0.120.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.120.0 // indirect
	go.opentelemetry.io/collector/pdata/testdata v0.120.0 // indirect
	go.opentelemetry.io/collector/pipeline v0.120.0 // indirect
	go.opentelemetry.io/collector/pipeline/xpipeline v0.120.0 // indirect
	go.opentelemetry.io/collector/processor/xprocessor v0.120.0 // indirect
	go.opentelemetry.io/collector/receiver/receivertest v0.120.0 // indirect
	go.opentelemetry.io/collector/receiver/xreceiver v0.120.0 // indirect
	go.opentelemetry.io/collector/semconv v0.120.0 // indirect
	go.opentelemetry.io/collector/service v0.120.0 // indirect
	go.opentelemetry.io/contrib/bridges/otelzap v0.9.0 // indirect
	go.opentelemetry.io/contrib/config v0.14.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.59.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.59.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.34.0 // indirect
	go.opentelemetry.io/contrib/zpages v0.59.0 // indirect
	go.opentelemetry.io/otel v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.10.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.10.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.56.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.10.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.34.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.34.0 // indirect
	go.opentelemetry.io/otel/log v0.10.0 // indirect
	go.opentelemetry.io/otel/metric v1.34.0 // indirect
	go.opentelemetry.io/otel/sdk v1.34.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.10.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.34.0 // indirect
	go.opentelemetry.io/otel/trace v1.34.0 // indirect
	go.opentelemetry.io/proto/otlp v1.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
	golang.org/x/net v0.35.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	golang.org/x/time v0.6.0 // indirect
	gonum.org/v1/gonum v0.15.1 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.5 // indirect
	gopkg.in/ini.v1 v1.67.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// replace (
// 	go.opentelemetry.io/collector => ../
// 	go.opentelemetry.io/collector/client => ../client
// 	go.opentelemetry.io/collector/component => ../component
// 	go.opentelemetry.io/collector/component/componentstatus => ../component/componentstatus
// 	go.opentelemetry.io/collector/component/componenttest => ../component/componenttest
// 	go.opentelemetry.io/collector/config/configauth => ../config/configauth
// 	go.opentelemetry.io/collector/config/configcompression => ../config/configcompression
// 	go.opentelemetry.io/collector/config/configgrpc => ../config/configgrpc
// 	go.opentelemetry.io/collector/config/confighttp => ../config/confighttp
// 	go.opentelemetry.io/collector/config/confignet => ../config/confignet
// 	go.opentelemetry.io/collector/config/configopaque => ../config/configopaque
// 	go.opentelemetry.io/collector/config/configretry => ../config/configretry
// 	go.opentelemetry.io/collector/config/configtelemetry => ../config/configtelemetry
// 	go.opentelemetry.io/collector/config/configtls => ../config/configtls
// 	go.opentelemetry.io/collector/config/internal => ../config/internal
// 	go.opentelemetry.io/collector/confmap => ../confmap
// 	go.opentelemetry.io/collector/confmap/provider/envprovider => ../confmap/provider/envprovider
// 	go.opentelemetry.io/collector/confmap/provider/fileprovider => ../confmap/provider/fileprovider
// 	go.opentelemetry.io/collector/confmap/provider/httpprovider => ../confmap/provider/httpprovider
// 	go.opentelemetry.io/collector/confmap/provider/httpsprovider => ../confmap/provider/httpsprovider
// 	go.opentelemetry.io/collector/confmap/provider/yamlprovider => ../confmap/provider/yamlprovider
// 	go.opentelemetry.io/collector/confmap/xconfmap => ../confmap/xconfmap
// 	go.opentelemetry.io/collector/connector => ../connector
// 	go.opentelemetry.io/collector/connector/connectortest => ../connector/connectortest
// 	go.opentelemetry.io/collector/connector/forwardconnector => ../connector/forwardconnector
// 	go.opentelemetry.io/collector/connector/xconnector => ../connector/xconnector
// 	go.opentelemetry.io/collector/consumer => ../consumer
// 	go.opentelemetry.io/collector/consumer/consumererror => ../consumer/consumererror
// 	go.opentelemetry.io/collector/consumer/consumertest => ../consumer/consumertest
// 	go.opentelemetry.io/collector/consumer/xconsumer => ../consumer/xconsumer
// 	go.opentelemetry.io/collector/exporter => ../exporter
// 	go.opentelemetry.io/collector/exporter/debugexporter => ../exporter/debugexporter
// 	go.opentelemetry.io/collector/exporter/exporterhelper/xexporterhelper => ../exporter/exporterhelper/xexporterhelper
// 	go.opentelemetry.io/collector/exporter/exportertest => ../exporter/exportertest
// 	go.opentelemetry.io/collector/exporter/otlpexporter => ../exporter/otlpexporter
// 	go.opentelemetry.io/collector/exporter/otlphttpexporter => ../exporter/otlphttpexporter
// 	go.opentelemetry.io/collector/exporter/xexporter => ../exporter/xexporter
// 	go.opentelemetry.io/collector/extension => ../extension
// 	go.opentelemetry.io/collector/extension/auth => ../extension/auth
// 	go.opentelemetry.io/collector/extension/extensioncapabilities => ../extension/extensioncapabilities
// 	go.opentelemetry.io/collector/extension/extensiontest => ../extension/extensiontest
// 	go.opentelemetry.io/collector/extension/memorylimiterextension => ../extension/memorylimiterextension
// 	go.opentelemetry.io/collector/extension/xextension => ../extension/xextension
// 	go.opentelemetry.io/collector/extension/zpagesextension => ../extension/zpagesextension
// 	go.opentelemetry.io/collector/featuregate => ../featuregate
// 	go.opentelemetry.io/collector/filter => ../filter
// 	go.opentelemetry.io/collector/internal/fanoutconsumer => ../internal/fanoutconsumer
// 	go.opentelemetry.io/collector/internal/memorylimiter => ../internal/memorylimiter
// 	go.opentelemetry.io/collector/internal/sharedcomponent => ../internal/sharedcomponent
// 	go.opentelemetry.io/collector/otelcol => ../otelcol
// 	go.opentelemetry.io/collector/pdata => ../pdata
// 	go.opentelemetry.io/collector/pdata/pprofile => ../pdata/pprofile
// 	go.opentelemetry.io/collector/pdata/testdata => ../pdata/testdata
// 	go.opentelemetry.io/collector/pipeline => ../pipeline
// 	go.opentelemetry.io/collector/pipeline/xpipeline => ../pipeline/xpipeline
// 	go.opentelemetry.io/collector/processor => ../processor
// 	go.opentelemetry.io/collector/processor/batchprocessor => ../processor/batchprocessor
// 	go.opentelemetry.io/collector/processor/memorylimiterprocessor => ../processor/memorylimiterprocessor
// 	go.opentelemetry.io/collector/processor/processortest => ../processor/processortest
// 	go.opentelemetry.io/collector/processor/xprocessor => ../processor/xprocessor
// 	go.opentelemetry.io/collector/receiver => ../receiver
// 	go.opentelemetry.io/collector/receiver/nopreceiver => ../receiver/nopreceiver
// 	go.opentelemetry.io/collector/receiver/otlpreceiver => ../receiver/otlpreceiver
// 	go.opentelemetry.io/collector/receiver/receivertest => ../receiver/receivertest
// 	go.opentelemetry.io/collector/receiver/xreceiver => ../receiver/xreceiver
// 	go.opentelemetry.io/collector/scraper => ../scraper
// 	go.opentelemetry.io/collector/scraper/scraperhelper => ../scraper/scraperhelper
// 	go.opentelemetry.io/collector/scraper/scrapertest => ../scraper/scrapertest
// 	go.opentelemetry.io/collector/semconv => ../semconv
// 	go.opentelemetry.io/collector/service => ../service
// )
