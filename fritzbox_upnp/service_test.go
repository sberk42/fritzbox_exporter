package fritzbox_upnp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testDesc = `<?xml version="1.0"?>
<root xmlns="urn:dslforum-org:device-1-0">
<device>
<deviceType>urn:dslforum-org:device:InternetGatewayDevice:1</deviceType>
<serviceList>
<service>
<serviceType>%s</serviceType>
<controlURL>/upnp/control/test</controlURL>
<SCPDURL>/testSCPD.xml</SCPDURL>
</service>
</serviceList>
</device>
</root>`

const testSCPD = `<?xml version="1.0"?>
<scpd xmlns="urn:dslforum-org:service-1-0">
<actionList>
<action><name>GetInfo</name></action>
</actionList>
</scpd>`

const (
	igdService  = "urn:schemas-upnp-org:service:WANIPConnection:1"
	tr64Service = "urn:dslforum-org:service:DeviceInfo:1"
)

func newTestDevice(t *testing.T, igdStatus int) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/igddesc.xml", func(w http.ResponseWriter, _ *http.Request) {
		if igdStatus != http.StatusOK {
			http.Error(w, "<HTML><BODY>error<HR></BODY></HTML>", igdStatus)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(testDesc, igdService)))
	})
	mux.HandleFunc("/tr64desc.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(testDesc, tr64Service)))
	})
	mux.HandleFunc("/testSCPD.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(testSCPD))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoadServices(t *testing.T) {
	tests := []struct {
		name      string
		igdStatus int
		want      []string
		wantErr   bool
	}{
		{name: "both descriptions", igdStatus: http.StatusOK, want: []string{igdService, tr64Service}},
		{name: "no IGD description", igdStatus: http.StatusNotFound, want: []string{tr64Service}},
		{name: "IGD server error", igdStatus: http.StatusInternalServerError, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestDevice(t, tt.igdStatus)

			root, err := LoadServices(srv.URL, "", "", false)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(root.Services) != len(tt.want) {
				t.Fatalf("got %d services, want %d", len(root.Services), len(tt.want))
			}
			for _, s := range tt.want {
				if _, ok := root.Services[s]; !ok {
					t.Errorf("service %s missing", s)
				}
			}
		})
	}
}
