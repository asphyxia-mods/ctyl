package registry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetagUploadsExactBytes(t *testing.T) {
	// Deliberately unusual formatting: any re-serialization would change it.
	raw := []byte(`{"schemaVersion":2,  "mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	digest := digestOf(raw)
	var putBody []byte
	var putType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/o/ctyl/manifests/"+digest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Write(raw)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/o/ctyl/manifests/java_21":
			putBody, _ = io.ReadAll(r.Body)
			putType = r.Header.Get("Content-Type")
			w.Header().Set("Docker-Content-Digest", digestOf(putBody))
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	repo := strings.Replace(strings.TrimPrefix(srv.URL, "http://"), "127.0.0.1", "localhost", 1) + "/o/ctyl"
	if err := Retag(repo, digest, "java_21"); err != nil {
		t.Fatal(err)
	}
	if string(putBody) != string(raw) || putType != "application/vnd.oci.image.index.v1+json" {
		t.Errorf("uploaded %q as %q", putBody, putType)
	}
	if err := Retag(repo, "sha256:"+strings.Repeat("0", 64), "java_21"); err == nil {
		t.Error("retag of a missing digest succeeded")
	}
}
