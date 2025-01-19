package main

// Copyright 2016 Nils Decker
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/namsral/flag"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	lua "github.com/sberk42/fritzbox_exporter/fritzbox_lua"
	upnp "github.com/sberk42/fritzbox_exporter/fritzbox_upnp"
)

const serviceLoadRetryTime = 1 * time.Minute

// minimum TTL for cached results in seconds
const minCacheTTL = 30

var (
	flagTest    = flag.Bool("test", false, "print all available metrics to stdout")
	flagLuaTest = flag.Bool("testLua", false, "read luaTest.json file make all contained calls and dump results")
	flagCollect = flag.Bool("collect", false, "print configured metrics to stdout and exit")
	flagJSONOut = flag.String("json-out", "", "store metrics also to JSON file when running test")

	flagAddr           = flag.String("listen-address", "127.0.0.1:9042", "The address to listen on for HTTP requests.")
	flagMetricsFile    = flag.String("metrics-file", "metrics.json", "The JSON file with the metric definitions.")
	flagDisableLua     = flag.Bool("nolua", false, "disable collecting lua metrics")
	flagLuaMetricsFile = flag.String("lua-metrics-file", "metrics-lua.json", "The JSON file with the lua metric definitions.")

	flagGatewayURL       = flag.String("gateway-url", "http://fritz.box:49000", "The URL of the FRITZ!Box")
	flagGatewayLuaURL    = flag.String("gateway-luaurl", "http://fritz.box", "The URL of the FRITZ!Box UI")
	flagUsername         = flag.String("username", "", "The user for the FRITZ!Box UPnP service")
	flagPassword         = flag.String("password", "", "The password for the FRITZ!Box UPnP service")
	flagSessionApi       = flag.String("sessionapi", "v1", "Use the v1 md5 authentication (default) or the v2 pbkdf2 authentication")
	flagGatewayVerifyTLS = flag.Bool("verifyTls", false, "Verify the tls connection when connecting to the FRITZ!Box")
	flagLogLevel         = flag.String("log-level", "info", "The logging level. Can be error, warn, info, debug or trace")
)

var (
	collectErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fritzbox_exporter_collectErrors",
		Help: "Number of collection errors.",
	})
)
var (
	luaCollectErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fritzbox_exporter_luaCollectErrors",
		Help: "Number of lua collection errors.",
	})
)
var collectLuaResultsCached = prometheus.NewCounter(prometheus.CounterOpts{
	Name:        "fritzbox_exporter_results_cached",
	Help:        "Number of results taken from cache.",
	ConstLabels: prometheus.Labels{"Cache": "LUA"},
})
var collectUpnpResultsCached = prometheus.NewCounter(prometheus.CounterOpts{
	Name:        "fritzbox_exporter_results_cached",
	Help:        "Number of results taken from cache.",
	ConstLabels: prometheus.Labels{"Cache": "UPNP"},
})
var collectLuaResultsLoaded = prometheus.NewCounter(prometheus.CounterOpts{
	Name:        "fritzbox_exporter_results_loaded",
	Help:        "Number of results loaded from fritzbox.",
	ConstLabels: prometheus.Labels{"Cache": "LUA"},
})
var collectUpnpResultsLoaded = prometheus.NewCounter(prometheus.CounterOpts{
	Name:        "fritzbox_exporter_results_loaded",
	Help:        "Number of results loaded from fritzbox.",
	ConstLabels: prometheus.Labels{"Cache": "UPNP"},
})

// JSONPromDesc metric description loaded from JSON
type JSONPromDesc struct {
	FqName           string            `json:"fqName"`
	Help             string            `json:"help"`
	VarLabels        []string          `json:"varLabels"`
	FixedLabels      map[string]string `json:"fixedLabels"`
	fixedLabelValues string            // neeeded to create uniq lookup key when reporting
}

// ActionArg argument for upnp action
type ActionArg struct {
	Name           string `json:"Name"`
	IsIndex        bool   `json:"IsIndex"`
	ProviderAction string `json:"ProviderAction"`
	Value          string `json:"Value"`
}

// Metric upnp metric
type Metric struct {
	// initialized loading JSON
	Service        string       `json:"service"`
	Action         string       `json:"action"`
	ActionArgument *ActionArg   `json:"actionArgument"`
	Result         string       `json:"result"`
	OkValue        string       `json:"okValue"`
	PromDesc       JSONPromDesc `json:"promDesc"`
	PromType       string       `json:"promType"`
	CacheEntryTTL  int64        `json:"cacheEntryTTL"`

	// initialized at startup
	Desc       *prometheus.Desc
	MetricType prometheus.ValueType
}

// LuaTest JSON struct for API tests
type LuaTest struct {
	Path   string `json:"path"`
	Params string `json:"params"`
}

// LuaLabelRename struct
type LuaLabelRename struct {
	MatchRegex  string `json:"matchRegex"`
	RenameLabel string `json:"renameLabel"`
}

// LuaMetric struct
type LuaMetric struct {
	// initialized loading JSON
	Path          string       `json:"path"`
	Params        string       `json:"params"`
	ResultPath    string       `json:"resultPath"`
	ResultKey     string       `json:"resultKey"`
	OkValue       string       `json:"okValue"`
	PromDesc      JSONPromDesc `json:"promDesc"`
	PromType      string       `json:"promType"`
	CacheEntryTTL int64        `json:"cacheEntryTTL"`

	// initialized at startup
	Desc         *prometheus.Desc
	MetricType   prometheus.ValueType
	LuaPage      lua.LuaPage
	LuaMetricDef lua.LuaMetricValueDefinition
}

// LuaMetricsFile json struct
type LuaMetricsFile struct {
	LabelRenames []LuaLabelRename `json:"labelRenames"`
	Metrics      []*LuaMetric     `json:"metrics"`
}

type upnpCacheEntry struct {
	Timestamp int64
	Result    *upnp.Result
}

type luaCacheEntry struct {
	Timestamp int64
	Result    *map[string]interface{}
}

var metrics []*Metric
var luaMetrics []*LuaMetric
var upnpCache map[string]*upnpCacheEntry
var luaCache map[string]*luaCacheEntry

// FritzboxCollector main struct
type FritzboxCollector struct {
	URL       string
	Gateway   string
	Username  string
	Password  string
	VerifyTls bool

	// support for lua collector
	LuaSession   *lua.LuaSession
	LabelRenames *[]lua.LabelRename

	sync.Mutex // protects Root
	Root       *upnp.Root

	// health checks
	ReadyStatus string
	Ready       bool
	LiveStatus  string
	Live        bool
}

// simple ResponseWriter to collect output
type testResponseWriter struct {
	header     http.Header
	statusCode int
	body       bytes.Buffer
}

func (w *testResponseWriter) Header() http.Header {
	return w.header
}

func (w *testResponseWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *testResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *testResponseWriter) String() string {
	return w.body.String()
}

// LoadServices tries to load the service information. Retries until success.
func (fc *FritzboxCollector) LoadServices() {
	fc.ReadyStatus = "loading services"
	fc.LiveStatus = "waiting for services loading"
	for {
		root, err := upnp.LoadServices(fc.URL, fc.Username, fc.Password, fc.VerifyTls)
		if err != nil {
			fc.ReadyStatus = "failed to load services"
			logrus.Errorf("cannot load services: %s", err)

			time.Sleep(serviceLoadRetryTime)
			continue
		}

		logrus.Info("services loaded")
		fc.ReadyStatus = "services loaded"
		fc.Ready = true

		// set also live
		fc.LiveStatus = "ready"
		fc.Live = true

		fc.Lock()
		fc.Root = root
		fc.Unlock()
		return
	}
}

// healthcheck functions
func (fc *FritzboxCollector) ReadynessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if fc.Ready {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	io.WriteString(w, "{ \"status\":\""+fc.ReadyStatus+"\"}")
}

func (fc *FritzboxCollector) LivenessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if fc.Live {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	// is there something in promhttp that we could check?

	io.WriteString(w, "{ \"status\":\""+fc.LiveStatus+"\"}")
}

// Describe describe metric
func (fc *FritzboxCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range metrics {
		ch <- m.Desc
	}
}

func (fc *FritzboxCollector) reportMetric(ch chan<- prometheus.Metric, m *Metric, result upnp.Result, dupCache map[string]bool) {

	val, ok := result[m.Result]
	if !ok {
		logrus.Debugf("%s.%s has no result %s", m.Service, m.Action, m.Result)
		collectErrors.Inc()
		return
	}

	var floatval float64
	switch tval := val.(type) {
	case uint64:
		floatval = float64(tval)
	case bool:
		if tval {
			floatval = 1
		} else {
			floatval = 0
		}
	case string:
		if tval == m.OkValue {
			floatval = 1
		} else {
			floatval = 0
		}
	default:
		logrus.Warnf("unknown type: %s", val)
		collectErrors.Inc()
		return
	}

	labels := make([]string, len(m.PromDesc.VarLabels))
	for i, l := range m.PromDesc.VarLabels {
		if l == "gateway" {
			labels[i] = fc.Gateway
		} else {
			lval, ok := result[l]
			if !ok {
				logrus.Warnf("%s.%s has no resul for label %s", m.Service, m.Action, l)
				lval = ""
			}

			// convert hostname and MAC tolower to avoid problems with labels
			if l == "HostName" || l == "MACAddress" {
				labels[i] = strings.ToLower(fmt.Sprintf("%v", lval))
			} else {
				labels[i] = fmt.Sprintf("%v", lval)
			}
		}
	}

	// check for duplicate labels to prevent collection failure
	key := m.PromDesc.FqName + ":" + m.PromDesc.fixedLabelValues + strings.Join(labels, ",")
	if dupCache[key] {
		logrus.Debugf("%s.%s reported before as: %s\n", m.Service, m.Action, key)
		collectErrors.Inc()
		return
	}
	dupCache[key] = true

	metric, err := prometheus.NewConstMetric(m.Desc, m.MetricType, floatval, labels...)
	if err != nil {
		logrus.Errorf("Can not create metric %s.%s: %s", m.Service, m.Action, err.Error())
	} else {
		ch <- metric
	}
}

func (fc *FritzboxCollector) getActionResult(metric *Metric, actionName string, actionArg *upnp.ActionArgument) (upnp.Result, error) {

	key := metric.Service + "|" + actionName

	// for calls with argument also add argument name and value to key
	if actionArg != nil {
		key += "|" + actionArg.Name + "|" + fmt.Sprintf("%v", actionArg.Value)
	}

	now := time.Now().Unix()

	cacheEntry := upnpCache[key]
	if cacheEntry == nil {
		cacheEntry = &upnpCacheEntry{}
		upnpCache[key] = cacheEntry
	} else if now-cacheEntry.Timestamp > metric.CacheEntryTTL {
		cacheEntry.Result = nil
	}

	if cacheEntry.Result == nil {
		service, ok := fc.Root.Services[metric.Service]
		if !ok {
			return nil, fmt.Errorf("service %s not found", metric.Service)
		}

		action, ok := service.Actions[actionName]
		if !ok {
			return nil, fmt.Errorf("action %s not found in service %s", actionName, metric.Service)
		}

		data, err := action.Call(actionArg)

		if err != nil {
			return nil, err
		}

		cacheEntry.Timestamp = now
		cacheEntry.Result = &data
		collectUpnpResultsCached.Inc()
	} else {
		collectUpnpResultsLoaded.Inc()
	}

	return *cacheEntry.Result, nil
}

// Collect collect upnp metrics
func (fc *FritzboxCollector) Collect(ch chan<- prometheus.Metric) {
	fc.Lock()
	root := fc.Root
	fc.Unlock()

	if root == nil {
		// Services not loaded yet
		return
	}

	// create cache for duplicate lookup, to prevent collection errors
	var dupCache = make(map[string]bool)

	for _, m := range metrics {
		var actArg *upnp.ActionArgument
		if m.ActionArgument != nil {
			aa := m.ActionArgument
			var value interface{}
			value = aa.Value

			if aa.ProviderAction != "" {
				provRes, err := fc.getActionResult(m, aa.ProviderAction, nil)

				if err != nil {
					logrus.Warnf("Error getting provider action %s result for %s.%s: %s", aa.ProviderAction, m.Service, m.Action, err.Error())
					collectErrors.Inc()
					continue
				}

				var ok bool
				value, ok = provRes[aa.Value] // Value contains the result name for provider actions
				if !ok {
					logrus.Warnf("provider action %s for %s.%s has no result", m.Service, m.Action, aa.Value)
					collectErrors.Inc()
					continue
				}
			}

			if aa.IsIndex {
				sval := fmt.Sprintf("%v", value)
				count, err := strconv.Atoi(sval)
				if err != nil {
					logrus.Warn(err.Error())
					collectErrors.Inc()
					continue
				}

				for i := 0; i < count; i++ {
					actArg = &upnp.ActionArgument{Name: aa.Name, Value: i}
					result, err := fc.getActionResult(m, m.Action, actArg)

					if err != nil {
						logrus.Errorf("can not get result for %s: %s", m.Action, err)
						collectErrors.Inc()
						continue
					}

					fc.reportMetric(ch, m, result, dupCache)
				}

				continue
			} else {
				actArg = &upnp.ActionArgument{Name: aa.Name, Value: value}
			}
		}

		result, err := fc.getActionResult(m, m.Action, actArg)

		if err != nil {
			logrus.Warnf("can not collect metrics: %s", err)
			collectErrors.Inc()
			continue
		}

		fc.reportMetric(ch, m, result, dupCache)
	}

	// if lua is enabled now also collect metrics
	if fc.LuaSession != nil {
		fc.collectLua(ch, dupCache)
	}
}

func (fc *FritzboxCollector) collectLua(ch chan<- prometheus.Metric, dupCache map[string]bool) {
	// create a map for caching results
	now := time.Now().Unix()

	for _, lm := range luaMetrics {
		key := lm.Path + "_" + lm.Params

		cacheEntry := luaCache[key]
		if cacheEntry == nil {
			cacheEntry = &luaCacheEntry{}
			luaCache[key] = cacheEntry
		} else if now-cacheEntry.Timestamp > lm.CacheEntryTTL {
			cacheEntry.Result = nil
		}

		if cacheEntry.Result == nil {
			pageData, err := fc.LuaSession.LoadData(lm.LuaPage)

			if err != nil {
				logrus.Errorf("Can not load %s for %s.%s: %s", lm.Path, lm.ResultPath, lm.ResultKey, err.Error())
				luaCollectErrors.Inc()
				fc.LuaSession.SID = "" // clear SID in case of error, so force reauthentication
				continue
			}

			var data map[string]interface{}
			data, err = lua.ParseJSON(pageData)
			if err != nil {
				logrus.Errorf("Can not parse JSON from %s for %s.%s: %s", lm.Path, lm.ResultPath, lm.ResultKey, err.Error())
				luaCollectErrors.Inc()
				fc.LuaSession.SID = "" // clear SID in case of error, so force reauthentication
				continue
			}

			cacheEntry.Result = &data
			cacheEntry.Timestamp = now
			collectLuaResultsLoaded.Inc()
		} else {
			collectLuaResultsCached.Inc()
		}

		metricVals, err := lua.GetMetrics(fc.LabelRenames, *cacheEntry.Result, lm.LuaMetricDef)

		if err != nil {
			logrus.Errorf("Can not get metric values for %s.%s: %s", lm.ResultPath, lm.ResultKey, err.Error())
			luaCollectErrors.Inc()
			fc.LuaSession.SID = ""  // clear SID in case of error, so force reauthentication
			cacheEntry.Result = nil // don't use invalid results for cache
			continue
		}

		for _, mv := range metricVals {
			fc.reportLuaMetric(ch, lm, mv, dupCache)
		}
	}
}

func (fc *FritzboxCollector) reportLuaMetric(ch chan<- prometheus.Metric, lm *LuaMetric, value lua.LuaMetricValue, dupCache map[string]bool) {

	labels := make([]string, len(lm.PromDesc.VarLabels))
	for i, l := range lm.PromDesc.VarLabels {
		if l == "gateway" {
			labels[i] = fc.Gateway
		} else {
			lval, ok := value.Labels[l]
			if !ok {
				logrus.Warnf("%s.%s from %s?%s has no resul for label %s", lm.ResultPath, lm.ResultKey, lm.Path, lm.Params, l)
				lval = ""
			}

			// convert hostname and MAC tolower to avoid problems with labels
			if l == "HostName" || l == "MACAddress" {
				labels[i] = strings.ToLower(fmt.Sprintf("%v", lval))
			} else {
				labels[i] = fmt.Sprintf("%v", lval)
			}
		}
	}

	// check for duplicate labels to prevent collection failure
	key := lm.PromDesc.FqName + ":" + lm.PromDesc.fixedLabelValues + strings.Join(labels, ",")
	if dupCache[key] {
		logrus.Errorf("%s.%s reported before as: %s\n", lm.ResultPath, lm.ResultPath, key)
		luaCollectErrors.Inc()
		return
	}
	dupCache[key] = true

	metric, err := prometheus.NewConstMetric(lm.Desc, lm.MetricType, value.Value, labels...)
	if err != nil {
		logrus.Errorf("Can not create metric %s.%s: %s", lm.ResultPath, lm.ResultPath, err.Error())
	} else {
		ch <- metric
	}
}

func test() {
	root, err := upnp.LoadServices(*flagGatewayURL, *flagUsername, *flagPassword, *flagGatewayVerifyTLS)
	if err != nil {
		panic(err)
	}

	var newEntry = false
	var json bytes.Buffer
	json.WriteString("[\n")

	serviceKeys := []string{}
	for k := range root.Services {
		serviceKeys = append(serviceKeys, k)
	}
	sort.Strings(serviceKeys)
	for _, k := range serviceKeys {
		s := root.Services[k]
		logrus.Infof("Service: %s (Url: %s)\n", k, s.ControlURL)

		actionKeys := []string{}
		for l := range s.Actions {
			actionKeys = append(actionKeys, l)
		}
		sort.Strings(actionKeys)
		for _, l := range actionKeys {
			a := s.Actions[l]
			logrus.Debugf("%s - arguments: variable [direction] (soap name, soap type)", a.Name)
			for _, arg := range a.Arguments {
				sv := arg.StateVariable
				logrus.Debugf("%s [%s] (%s, %s)", arg.RelatedStateVariable, arg.Direction, arg.Name, sv.DataType)
			}

			if !a.IsGetOnly() {
				logrus.Debugf("%s - not calling, since arguments required or no output", a.Name)
				continue
			}

			// only create JSON for Get
			// TODO also create JSON templates for input actionParams
			for _, arg := range a.Arguments {
				// create new json entry
				if newEntry {
					json.WriteString(",\n")
				} else {
					newEntry = true
				}

				json.WriteString("\t{\n\t\t\"service\": \"")
				json.WriteString(k)
				json.WriteString("\",\n\t\t\"action\": \"")
				json.WriteString(a.Name)
				json.WriteString("\",\n\t\t\"result\": \"")
				json.WriteString(arg.RelatedStateVariable)
				json.WriteString("\"\n\t}")
			}

			logrus.Debugf("%s - calling - results: variable: value", a.Name)
			res, err := a.Call(nil)

			if err != nil {
				logrus.Warnf("FAILED:%s", err)
				continue
			}

			for _, arg := range a.Arguments {
				logrus.Debugf("%s: %v", arg.RelatedStateVariable, res[arg.StateVariable.Name])
			}
		}
	}

	json.WriteString("\n]")

	if *flagJSONOut != "" {
		err := os.WriteFile(*flagJSONOut, json.Bytes(), 0644)
		if err != nil {
			logrus.Warnf("Failed writing JSON file '%s': %s\n", *flagJSONOut, err.Error())
		}
	}
}

func testLua() {

	jsonData, err := os.ReadFile("luaTest.json")
	if err != nil {
		logrus.Error("Can not read luaTest.json: ", err)
		return
	}

	var luaTests []LuaTest
	err = json.Unmarshal(jsonData, &luaTests)
	if err != nil {
		logrus.Error("Can not parse luaTest JSON: ", err)
		return
	}

	// create session struct and init params
	luaSession := lua.LuaSession{BaseURL: *flagGatewayLuaURL, Username: *flagUsername, Password: *flagPassword, ApiVer: *flagSessionApi}

	for _, test := range luaTests {
		page := lua.LuaPage{Path: test.Path, Params: test.Params}
		pageData, err := luaSession.LoadData(page)

		if err != nil {
			logrus.Errorf("Testing %s (%s) failed: %s", test.Path, test.Params, err.Error())
		} else {
			logrus.Infof("Testing %s (%s) successful: %s", test.Path, test.Params, string(pageData))
		}
	}
}

func getValueType(vt string) prometheus.ValueType {
	switch vt {
	case "CounterValue":
		return prometheus.CounterValue
	case "GaugeValue":
		return prometheus.GaugeValue
	case "UntypedValue":
		return prometheus.UntypedValue
	}

	return prometheus.UntypedValue
}

func main() {
	flag.Parse()
	level, e := logrus.ParseLevel(*flagLogLevel)
	if e != nil {
		logrus.Warnf("Can not parse log level: %s use INFO", e)
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)

	u, err := url.Parse(*flagGatewayURL)
	if err != nil {
		logrus.Errorf("invalid URL: %s", err.Error())
		return
	}

	// I have tested authentication against both hostname and ipv4 ip.
	// It only seems to work against the latter. It doesn't really
	// make sense but it is how it is.
	//
	// Here we try to figure out what ipv4 resolves to fritz.box
	lu, err := url.Parse(*flagGatewayLuaURL)
	if err != nil && *flagLuaTest {
		logrus.Errorf("invalid LUA URL: %s", err.Error())
		return
	}
	luahost := lu.Hostname()
	if strings.Contains(luahost, "fritz.box") {
		ips, err := net.LookupIP("fritz.box")
		if err != nil {
			logrus.Errorf("could not lookup fritz.box: %s", err.Error())
			return
		}
		for _, ip := range ips {
			if ip.To4() != nil {
				luahost = ip.String()
				logrus.Infoln("Ip: ", ip)
			}
		}
	}
	*flagGatewayLuaURL = fmt.Sprintf("%s://%s", lu.Scheme, luahost)

	if *flagTest {
		test()
		return
	}

	if *flagLuaTest {
		testLua()
		return
	}

	// read metrics
	jsonData, err := os.ReadFile(*flagMetricsFile)
	if err != nil {
		logrus.Errorf("error reading metric file: %s", err.Error())
		return
	}

	err = json.Unmarshal(jsonData, &metrics)
	if err != nil {
		logrus.Errorf("error parsing JSON: %s", err.Error())
		return
	}

	// create a map for caching results
	upnpCache = make(map[string]*upnpCacheEntry)

	var luaSession *lua.LuaSession
	var luaLabelRenames *[]lua.LabelRename
	if !*flagDisableLua {
		jsonData, err := os.ReadFile(*flagLuaMetricsFile)
		if err != nil {
			logrus.Error("error reading lua metric file:", err)
			return
		}

		var lmf *LuaMetricsFile
		err = json.Unmarshal(jsonData, &lmf)
		if err != nil {
			logrus.Error("error parsing lua JSON:", err)
			return
		}

		// create a map for caching results
		luaCache = make(map[string]*luaCacheEntry)

		// init label renames
		lblRen := make([]lua.LabelRename, 0)
		for _, ren := range lmf.LabelRenames {
			regex, err := regexp.Compile(ren.MatchRegex)

			if err != nil {
				logrus.Error("error compiling lua rename regex:", err)
				return
			}

			lblRen = append(lblRen, lua.LabelRename{Pattern: *regex, Name: ren.RenameLabel})
		}
		luaLabelRenames = &lblRen

		// init metrics
		luaMetrics = lmf.Metrics
		for _, lm := range luaMetrics {
			pd := &lm.PromDesc

			// make labels lower case
			labels := make([]string, len(pd.VarLabels))
			for i, l := range pd.VarLabels {
				labels[i] = strings.ToLower(l)
			}

			// create fixed labels values
			pd.fixedLabelValues = ""
			for _, flv := range pd.FixedLabels {
				pd.fixedLabelValues += flv + ","
			}

			lm.Desc = prometheus.NewDesc(pd.FqName, pd.Help, labels, pd.FixedLabels)
			lm.MetricType = getValueType(lm.PromType)

			lm.LuaPage = lua.LuaPage{
				Path:   lm.Path,
				Params: lm.Params,
			}

			lm.LuaMetricDef = lua.LuaMetricValueDefinition{
				Path:    lm.ResultPath,
				Key:     lm.ResultKey,
				OkValue: lm.OkValue,
				Labels:  pd.VarLabels,
			}

			// init TTL
			if lm.CacheEntryTTL < minCacheTTL {
				lm.CacheEntryTTL = minCacheTTL
			}
		}

		luaSession = &lua.LuaSession{
			BaseURL:  *flagGatewayLuaURL,
			Username: *flagUsername,
			Password: *flagPassword,
			ApiVer:   *flagSessionApi,
		}
	}

	// init metrics
	renw := regexp.MustCompile(`\W+`)
	for _, m := range metrics {
		pd := &m.PromDesc

		// make labels lower and replace - with _
		labels := make([]string, len(pd.VarLabels))
		for i, l := range pd.VarLabels {
			labels[i] = renw.ReplaceAllString(strings.ToLower(strings.ReplaceAll(l, "-", "_")), "")
		}

		// create fixed labels values
		pd.fixedLabelValues = ""
		for _, flv := range pd.FixedLabels {
			pd.fixedLabelValues += flv + ","
		}

		m.Desc = prometheus.NewDesc(pd.FqName, pd.Help, labels, pd.FixedLabels)
		m.MetricType = getValueType(m.PromType)

		// init TTL
		if m.CacheEntryTTL < minCacheTTL {
			m.CacheEntryTTL = minCacheTTL
		}
	}

	collector := &FritzboxCollector{
		URL:       *flagGatewayURL,
		Gateway:   u.Hostname(),
		Username:  *flagUsername,
		Password:  *flagPassword,
		VerifyTls: *flagGatewayVerifyTLS,

		LuaSession:   luaSession,
		LabelRenames: luaLabelRenames,
	}

	if *flagCollect {
		collector.LoadServices()

		prometheus.MustRegister(collector)
		prometheus.MustRegister(collectErrors)
		if luaSession != nil {
			prometheus.MustRegister(luaCollectErrors)
		}

		logrus.Infof("collecting metrics via http")

		// simulate HTTP request without starting actual http server
		writer := testResponseWriter{header: http.Header{}}
		request := http.Request{}
		promhttp.Handler().ServeHTTP(&writer, &request)

		logrus.Infof("Response:\n\n%s", writer.String())

		return
	}

	go collector.LoadServices()

	prometheus.MustRegister(collector)
	prometheus.MustRegister(collectErrors)
	prometheus.MustRegister(collectUpnpResultsCached)
	prometheus.MustRegister(collectUpnpResultsLoaded)

	if luaSession != nil {
		prometheus.MustRegister(luaCollectErrors)
		prometheus.MustRegister(collectLuaResultsCached)
		prometheus.MustRegister(collectLuaResultsLoaded)
	}

	http.Handle("/metrics", promhttp.Handler())
	logrus.Infof("metrics available at http://%s/metrics", *flagAddr)
	http.HandleFunc("/ready", collector.ReadynessHandler)
	logrus.Infof("readyness check available at http://%s/ready", *flagAddr)
	http.HandleFunc("/live", collector.LivenessHandler)
	logrus.Infof("liveness check available at http://%s/live", *flagAddr)

	logrus.Error(http.ListenAndServe(*flagAddr, nil))
}
