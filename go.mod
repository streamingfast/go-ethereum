module github.com/ethereum/go-ethereum

go 1.25.0

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.4
	github.com/BurntSushi/toml v1.6.0
	github.com/Microsoft/go-winio v0.6.2
	github.com/VictoriaMetrics/fastcache v1.13.2
	github.com/aws/aws-sdk-go-v2 v1.41.1
	github.com/aws/aws-sdk-go-v2/config v1.32.7
	github.com/aws/aws-sdk-go-v2/credentials v1.19.7
	github.com/aws/aws-sdk-go-v2/service/route53 v1.62.1
	github.com/cespare/cp v1.1.1
	github.com/cloudflare/cloudflare-go v0.116.0
	github.com/cockroachdb/pebble v1.1.5
	github.com/consensys/gnark-crypto v0.19.2
	github.com/crate-crypto/go-eth-kzg v1.4.0
	github.com/crate-crypto/go-ipa v0.0.0-20240724233137-53bbb0ceb27a
	github.com/davecgh/go-spew v1.1.1
	github.com/dchest/siphash v1.2.3
	github.com/deckarep/golang-set/v2 v2.8.0
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0
	github.com/donovanhide/eventsource v0.0.0-20210830082556-c59027999da0
	github.com/dop251/goja v0.0.0-20260106131823-651366fbe6e3
	github.com/emmansun/base64 v0.8.0
	github.com/ethereum/c-kzg-4844/v2 v2.1.5
	github.com/ethereum/go-bigmodexpfix v0.0.0-20250911101455-f9e208c548ab
	github.com/ethereum/go-verkle v0.2.2
	github.com/fatih/color v1.18.0
	github.com/ferranbt/fastssz v1.0.0
	github.com/fsnotify/fsnotify v1.9.0
	github.com/gballet/go-libpcsclite v0.0.0-20250918194357-1ec6f2e601c6
	github.com/gofrs/flock v0.13.0
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/golang/snappy v1.0.0
	github.com/google/gofuzz v1.2.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/graph-gophers/graphql-go v1.8.0
	github.com/hashicorp/go-bexpr v0.1.15
	github.com/holiman/billy v0.0.0-20250707135307-f2f9b9aae7db
	github.com/holiman/bloomfilter/v2 v2.0.3
	github.com/holiman/uint256 v1.3.2
	github.com/huin/goupnp v1.3.0
	github.com/influxdata/influxdb-client-go/v2 v2.14.0
	github.com/influxdata/influxdb1-client v0.0.0-20220302092344-a9ab5670611c
	github.com/jackpal/go-nat-pmp v1.0.2
	github.com/jedisct1/go-minisign v0.0.0-20241212093149-d2f9f49435c7
	github.com/karalabe/hid v1.0.1-0.20240306101548-573246063e52
	github.com/klauspost/compress v1.18.2
	github.com/kylelemons/godebug v1.1.0
	github.com/mattn/go-colorable v0.1.14
	github.com/mattn/go-isatty v0.0.20
	github.com/naoina/toml v0.1.2-0.20170918210437-9fafd6967416
	github.com/olekukonko/tablewriter v0.0.5
	github.com/peterh/liner v1.2.2
	github.com/pion/stun/v2 v2.0.0
	github.com/protolambda/bls12-381-util v0.1.0
	github.com/protolambda/zrnt v0.34.1
	github.com/protolambda/ztyp v0.2.2
	github.com/rs/cors v1.11.1
	github.com/shirou/gopsutil v3.21.11+incompatible
	github.com/status-im/keycard-go v0.3.3
	github.com/streamingfast/firehose-ethereum/types v0.0.0-20260119150111-65c739f81608
	github.com/stretchr/testify v1.11.1
	github.com/supranational/blst v0.3.16
	github.com/syndtr/goleveldb v1.0.1-0.20210819022825-2ae1ddf74ef7
	github.com/urfave/cli/v2 v2.27.7
	go.uber.org/automaxprocs v1.6.0
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.47.0
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96
	golang.org/x/sync v0.19.0
	golang.org/x/sys v0.40.0
	golang.org/x/text v0.33.0
	golang.org/x/time v0.14.0
	golang.org/x/tools v0.41.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/DataDog/zstd v1.5.7 // indirect
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.6 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.3.0 // indirect
	github.com/cockroachdb/errors v1.12.0 // indirect
	github.com/cockroachdb/fifo v0.0.0-20240816210425-c5d0cb0b6fc0 // indirect
	github.com/cockroachdb/logtags v0.0.0-20241215232642-bb51bb14a506 // indirect
	github.com/cockroachdb/redact v1.1.6 // indirect
	github.com/cockroachdb/tokenbucket v0.0.0-20250429170803-42689b6311bb // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/emicklei/dot v1.10.0 // indirect
	github.com/fjl/gencodec v0.1.1 // indirect
	github.com/garslo/gogen v0.0.0-20230926014519-f497ca02dd4c // indirect
	github.com/getsentry/sentry-go v0.40.0 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/pprof v0.0.0-20260111202518-71be6bfdd440 // indirect
	github.com/influxdata/line-protocol v0.0.0-20210922203350-b1ad95c89adf // indirect
	github.com/kilic/bls12-381 v0.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/minio/sha256-simd v1.0.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/mitchellh/pointerstructure v1.2.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/naoina/go-stringutil v0.1.0 // indirect
	github.com/oapi-codegen/runtime v1.0.0 // indirect
	github.com/pion/dtls/v2 v2.2.12 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/transport/v2 v2.2.10 // indirect
	github.com/pion/transport/v3 v3.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/planetscale/vtprotobuf v0.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.19.2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/xrash/smetrics v0.0.0-20250705151800-55b8f293f342 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	golang.org/x/mod v0.32.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

require (
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20260309015608-bb9dacb41b4e
	github.com/andybalholm/brotli v1.0.5
)

tool (
	github.com/fjl/gencodec
	golang.org/x/tools/cmd/stringer
	google.golang.org/protobuf/cmd/protoc-gen-go
)
