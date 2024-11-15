# Fritz!Box Upnp statistics exporter for prometheus

This exporter exports some variables from an 
[AVM Fritzbox](http://avm.de/produkte/fritzbox/)
to prometheus.

This exporter is tested with a Fritzbox 7590 software version 07.12, 07.20, 07.21, 07.25, 07.29, 07.50, 07.57 , 07.59 and 08.00.

The goal of the fork is:
  - [x] allow passing of username / password using evironment variable
  - [x] use https instead of http for communitcation with fritz.box
  - [x] move config of metrics to be exported to config file rather then code
  - [x] add config for additional metrics to collect (especially from TR-064 API)
  - [x] create a grafana dashboard consuming the additional metrics
  - [x] collect metrics from lua APIs not available in UPNP APIs
 
Other changes:
  - replaced digest authentication code with own implementation
  - improved error messages
  - test mode prints details about all SOAP Actions and their parameters
  - collect option to directly test collection of results
  - additional metrics to collect details about connected hosts and DECT devices
  - support to use results like hostname or MAC address as labels to metrics
  - support for metrics from lua APIs (e.g. CPU temperature, utilization, ...)
 

## Building

    go install github.com/sberk42/fritzbox_exporter@latest

## Running

Create a new user account for the exporter on the Fritzbox using the login credentials: 
```bash
USERNAME=your_fritzbox_username
PASSWORD=your_fritzbox_password
```
Grant this user access to the following features: 
FRITZ!Box settings, voice messages, fax messages, FRITZ!App Fon and call list, 
Smart Home, access to NAS content, and VPN.

In the configuration of the Fritzbox the option "Statusinformationen über UPnP übertragen" in the dialog "Heimnetz >
Heimnetzübersicht > Netzwerkeinstellungen" has to be enabled.

### Using docker

The image is available as package using:
`docker pull ghcr.io/sberk42/fritzbox_exporter/fritzbox_exporter:latest`
or you can build the container yourself: `docker build --tag fritzbox-prometheus-exporter:latest .`

Then start the container:

```bash
$ docker run -e 'USERNAME=your_fritzbox_username' \
    -e 'PASSWORD=your_fritzbox_password' \
    -e 'GATEWAY_URL="http://192.168.0.1:49000"' \
    -e 'LISTEN_ADDRESS="0.0.0.0:9042"' \
    fritzbox-prometheus-exporter:latest
```

I've you're getting `no such host` issues, define your FritzBox as DNS server for your docker container like this:

```bash
$ docker run --dns YOUR_FRITZBOX_IP \
    -e 'USERNAME=your_fritzbox_username' \
    -e 'PASSWORD=your_fritzbox_password' \
    -e 'GATEWAY_URL="http://192.168.0.1:49000"' \
    -e 'LISTEN_ADDRESS="0.0.0.0:9042"' \
    fritzbox-prometheus-exporter:latest
```

### Using docker-compose

Set your environment variables within the [docker-compose.yml](docker-compose.yml) file.  

Then start up the container using `docker-compose up -d`.

### Using the binary

Usage:

    $GOPATH/bin/fritzbox_exporter -h
    Usage of ./fritzbox_exporter:
      -gateway-url string
        The URL of the FRITZ!Box (default "http://fritz.box:49000")
      -gateway-luaurl string
        The URL of the FRITZ!Box UI (default "http://fritz.box")
      -metrics-file string
        The JSON file with the metric definitions. (default "metrics.json")
      -lua-metrics-file string
        The JSON file with the lua metric definitions. (default "metrics-lua.json")
      -test
        print all available SOAP calls and their results (if call possible) to stdout
      -json-out string
        store metrics also to JSON file when running test   
      -testLua
        read luaTest.json file make all contained calls and dump results
      -collect
        collect metrics once print to stdout and exit
      -nolua
        disable collecting lua metrics
      -username string
        The user for the FRITZ!Box UPnP service
      -password string
        The password for the FRITZ!Box UPnP service
      -listen-address string
        The address to listen on for HTTP requests. (default "127.0.0.1:9042")
    
    The password (needed for metrics from TR-064 API) can be passed over environment variables to test in shell:
    read -rs PASSWORD && export PASSWORD && ./fritzbox_exporter -username <user> -test; unset PASSWORD

## Exported metrics

start exporter and run
curl -s http://127.0.0.1:9042/metrics 

## Output of -test

The exporter prints all available Variables to stdout when called with the -test option.
These values are determined by parsing all services from http://fritz.box:49000/igddesc.xml and http://fritzbox:49000/tr64desc.xml (for TR64 username and password is needed!!!)

## Customizing metrics

The metrics to collect are no longer hard coded, but have been moved to the [metrics.json](metrics.json) and [metrics-lua.json](metrics-lua.json) files, so just adjust to your needs (for cable version also see [metrics-lua_cable.json](metrics-lua_cable.json)).
For a list of all the available metrics just execute the exporter with -test (username and password are needed for the TR-064 API!)
For lua metrics open UI in browser and check the json files used for the various screens.

For a list of all available metrics, see the dumps below (the format is the same as in the metrics.json file, so it can be used to easily add further metrics to retrieve):
- [FritzBox 6591 v7.29](all_available_metrics_6591_7.29.json)
- [FritzBox 6690 v7.57](all_available_metrics_6690_7.57.json)
- [FritzBox 7590 v7.12](all_available_metrics_7590_7.12.json)
- [FritzBox 7590 v7.20](all_available_metrics_7590_7.20.json)
- [FritzBox 7590 v7.25](all_available_metrics_7590_7.25.json)
- [FritzBox 7590 v7.29](all_available_metrics_7590_7.29.json)
- [FritzBox 7590 v7.50](all_available_metrics_7590_7.50.json)
- [FritzBox 7590 v7.57](all_available_metrics_7590_7.57.json)
- [FritzBox 7590 v7.59](all_available_metrics_7590_7.59.json) - same as 7.57
- [FritzBox 7590 v8.00](all_available_metrics_7590_8.00.json)
## Grafana Dashboard

The dashboard is now also published on [Grafana](https://grafana.com/grafana/dashboards/12579).
