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
	"fmt"
	"log"
	"net/url"
	"net/http"
	"sync"
	"time"
	"encoding/json"
	"io/ioutil"
	"sort"
	"bytes"

	"github.com/namsral/flag"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	
	upnp "github.com/sberk42/fritzbox_exporter/fritzbox_upnp"
)

const serviceLoadRetryTime = 1 * time.Minute

var (
	flag_test = flag.Bool("test", false, "print all available metrics to stdout")
	flag_jsonout = flag.String("json-out", "", "store metrics also to JSON file when running test")
	 
	flag_addr = flag.String("listen-address", "127.0.0.1:9042", "The address to listen on for HTTP requests.")
	flag_metrics_file = flag.String("metrics-file", "metrics.json", "The JSON file with the metric definitions.")

	flag_gateway_url  = flag.String("gateway-url", "http://fritz.box:49000", "The URL of the FRITZ!Box")
	flag_gateway_username = flag.String("username", "", "The user for the FRITZ!Box UPnP service")
	flag_gateway_password = flag.String("password", "", "The password for the FRITZ!Box UPnP service")
)

var (
	collect_errors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fritzbox_exporter_collect_errors",
		Help: "Number of collection errors.",
	})
)


type JSON_PromDesc struct {
	FqName	string			`json:"fqName"`
	Help	string			`json:"help"`
	VarLabels	[]string	`json:"varLabels"`
}

type Metric struct {
	// initialized loading JSON
	Service	string		`json:"service"`
	Action	string		`json:"action"`
	Result	string		`json:"result"`
	OkValue	string		`json:"okValue"`
	PromDesc	JSON_PromDesc		`json:"promDesc"`
	PromType	string			`json:"promType"`
	
	// initialized at startup
	Desc       *prometheus.Desc
	MetricType prometheus.ValueType
}

var metrics []*Metric;

type FritzboxCollector struct {
	Url      string
	Gateway  string
	Username string
	Password string

	sync.Mutex // protects Root
	Root       *upnp.Root
}

// LoadServices tries to load the service information. Retries until success.
func (fc *FritzboxCollector) LoadServices() {
	for {
		root, err := upnp.LoadServices(fc.Url, fc.Username, fc.Password)
		if err != nil {
			fmt.Printf("cannot load services: %s\n", err)

			time.Sleep(serviceLoadRetryTime)
			continue
		}

		fmt.Printf("services loaded\n")

		fc.Lock()
		fc.Root = root
		fc.Unlock()
		return
	}
}

func (fc *FritzboxCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range metrics {
		ch <- m.Desc
	}
}

func (fc *FritzboxCollector) Collect(ch chan<- prometheus.Metric) {
	fc.Lock()
	root := fc.Root
	fc.Unlock()

	if root == nil {
		// Services not loaded yet
		return
	}

	var err error
	var result_map = make(map[string]upnp.Result)

	for _, m := range metrics {
		m_key := m.Service+"|"+m.Action
		last_result	:= result_map[m_key];
		if last_result == nil {
			service, ok := root.Services[m.Service]
			if !ok {
				// TODO
				fmt.Println("cannot find service", m.Service)
				fmt.Println(root.Services)
				continue
			}
			action, ok := service.Actions[m.Action]
			if !ok {
				// TODO
				fmt.Println("cannot find action", m.Action)
				continue
			}

			last_result, err = action.Call()
			if err != nil {
				fmt.Println(err)
				collect_errors.Inc()
				continue
			}
			
			result_map[m_key]=last_result
		}

		val, ok := last_result[m.Result]
		if !ok {
			fmt.Println("result not found", m.Result)
			collect_errors.Inc()
			continue
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
			fmt.Println("unknown", val)
			collect_errors.Inc()
			continue

		}

		ch <- prometheus.MustNewConstMetric(
			m.Desc,
			m.MetricType,
			floatval,
			fc.Gateway,
		)
	}
}

func test() {
	root, err := upnp.LoadServices(*flag_gateway_url, *flag_gateway_username, *flag_gateway_password)
	if err != nil {
		panic(err)
	}
	
	var newEntry bool = false
	var json bytes.Buffer
	json.WriteString("[\n")

	serviceKeys := []string{}
	for k, _ := range root.Services {
		serviceKeys = append(serviceKeys, k)
	}
	sort.Strings(serviceKeys)
	for _, k := range serviceKeys {
		s := root.Services[k]
		fmt.Printf("Service: %s (Url: %s)\n", k, s.ControlUrl)
		
		actionKeys := []string{}
		for l, _ := range s.Actions {
			actionKeys = append(actionKeys, l)
		}
		sort.Strings(actionKeys)
		for _, l := range actionKeys {
			a := s.Actions[l]

			if !a.IsGetOnly() {
				continue
			}

			fmt.Printf("  %s\n", a.Name)
			res, err := a.Call()
			if err != nil {
				fmt.Printf("    FAILED:%s\n", err.Error())
				continue
			}

			for _, arg := range a.Arguments {
				fmt.Printf("    %s: %v\n", arg.RelatedStateVariable, res[arg.StateVariable.Name])

				// create new json entry				
				if(newEntry) {
					json.WriteString(",\n")
				} else {
					newEntry=true
				}
				
				json.WriteString("\t{\n\t\t\"service\": \"")
				json.WriteString(k)
				json.WriteString("\",\n\t\t\"action\": \"")
				json.WriteString(a.Name)
				json.WriteString("\",\n\t\t\"result\": \"")
				json.WriteString(arg.RelatedStateVariable)
				json.WriteString("\"\n\t}")
			}
		}
	}
	
	json.WriteString("\n]")
	
	if *flag_jsonout != "" {
		err := ioutil.WriteFile(*flag_jsonout, json.Bytes(), 0644)
		if err != nil {
			fmt.Printf("Failed writing JSON file '%s': %s\n", *flag_jsonout, err.Error())
		}			
	}
}

func getValueType(vt string) prometheus.ValueType {
	switch vt {
	case "CounterValue":
		return prometheus.CounterValue;
	case "GaugeValue":
		return prometheus.GaugeValue;
	case "UntypedValue":
		return prometheus.UntypedValue;
	}

	return prometheus.UntypedValue;
}

func main() {
	flag.Parse()

	u, err := url.Parse(*flag_gateway_url)
	if err != nil {
		fmt.Println("invalid URL:", err)
		return
	}

	if *flag_test {
		test()
		return
	}

	// read metrics 
	jsonData, err := ioutil.ReadFile(*flag_metrics_file)
	if err != nil {
		fmt.Println("error reading metric file:", err)
		return
	}

	err = json.Unmarshal(jsonData, &metrics)
	if err != nil {
		fmt.Println("error parsing JSON:", err)
		return
	}

	// init metrics
	for _, m := range metrics {
		pd := m.PromDesc
		m.Desc		= prometheus.NewDesc(pd.FqName, pd.Help, pd.VarLabels, nil)
		m.MetricType	= getValueType(m.PromType)
	}

	collector := &FritzboxCollector{
		Url:  *flag_gateway_url,
		Gateway: u.Hostname(),
		Username: *flag_gateway_username,
		Password: *flag_gateway_password,
	}

	go collector.LoadServices()

	prometheus.MustRegister(collector)
	prometheus.MustRegister(collect_errors)

	http.Handle("/metrics", promhttp.Handler())
	fmt.Printf("metrics available at http://%s/metrics\n", *flag_addr)
	log.Fatal(http.ListenAndServe(*flag_addr, nil))
}
